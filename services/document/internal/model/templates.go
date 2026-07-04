// Workspace templates (ADR 0118) — a reusable folder-tree definition.
//
// A template's Definition is a list of root TemplateNodes; each node
// scaffolds one folder (name, visibility, folder grants), zero or more
// placeholder documents (title, metadata defaults, tags), and children.
// String fields may reference variables as {{var_name}}, prompted for
// at provision time and substituted into folder names, doc titles, and
// metadata values.
package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Template tree limits — a provision runs in one tenant tx; these caps
// bound its size well below anything pathological.
const (
	TemplateMaxNodes = 200
	TemplateMaxDepth = 10
	TemplateMaxDocs  = 200
	// TemplateMaxBytes caps the raw definition JSON. Counts/depth alone
	// don't bound string sizes, and GET /templates returns every
	// definition in full — one megabyte-scale blob would degrade the
	// whole tenant's gallery.
	TemplateMaxBytes = 256 << 10
)

// WorkspaceTemplate is one saved template row.
type WorkspaceTemplate struct {
	TenantID    uuid.UUID       `json:"tenant_id"`
	ID          uuid.UUID       `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Definition  json.RawMessage `json:"definition"`
	CreatedBy   uuid.UUID       `json:"created_by"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// TemplateDefinition is the parsed Definition payload.
type TemplateDefinition struct {
	Nodes []TemplateNode `json:"nodes"`
}

// TemplateNode scaffolds one folder.
type TemplateNode struct {
	Name string `json:"name"`
	// Visibility: "" (shared, the default) or FolderPrivate.
	Visibility string `json:"visibility,omitempty"`
	// Metadata is inherited by every placeholder document provisioned
	// at this node and below (folders themselves carry no metadata);
	// deeper nodes and per-doc metadata override key-by-key.
	Metadata map[string]string `json:"metadata,omitempty"`
	Grants   []TemplateGrant   `json:"grants,omitempty"`
	Docs     []TemplateDoc     `json:"docs,omitempty"`
	Children []TemplateNode    `json:"children,omitempty"`
}

// TemplateGrant maps onto a folder_grants row at provision time.
type TemplateGrant struct {
	GranteeType string `json:"grantee_type"` // user | group
	GranteeID   string `json:"grantee_id"`   // UUID of the user/group
}

// TemplateDoc scaffolds one placeholder document (no version/bytes —
// content arrives later through the normal upload path).
type TemplateDoc struct {
	Title    string            `json:"title"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Tags     []string          `json:"tags,omitempty"`
}

// ParseTemplateDefinition unmarshals + validates a raw definition.
func ParseTemplateDefinition(raw json.RawMessage) (*TemplateDefinition, error) {
	if len(raw) > TemplateMaxBytes {
		return nil, fmt.Errorf("definition: larger than %d KiB", TemplateMaxBytes/1024)
	}
	var def TemplateDefinition
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("definition: %w", err)
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// Validate enforces structural limits + per-node invariants.
func (d *TemplateDefinition) Validate() error {
	if len(d.Nodes) == 0 {
		return fmt.Errorf("definition: at least one root folder is required")
	}
	nodes, docs := 0, 0
	var walk func(n *TemplateNode, depth int) error
	walk = func(n *TemplateNode, depth int) error {
		nodes++
		if nodes > TemplateMaxNodes {
			return fmt.Errorf("definition: more than %d folders", TemplateMaxNodes)
		}
		if depth > TemplateMaxDepth {
			return fmt.Errorf("definition: nesting deeper than %d levels", TemplateMaxDepth)
		}
		if strings.TrimSpace(n.Name) == "" {
			return fmt.Errorf("definition: folder name required at depth %d", depth)
		}
		if v := FolderVisibility(n.Visibility); n.Visibility != "" && v != FolderShared && v != FolderPrivate {
			return fmt.Errorf("definition: folder %q: visibility must be %q or %q", n.Name, FolderShared, FolderPrivate)
		}
		for _, g := range n.Grants {
			if g.GranteeType != "user" && g.GranteeType != "group" {
				return fmt.Errorf("definition: folder %q: grantee_type must be user or group", n.Name)
			}
			if _, err := uuid.Parse(g.GranteeID); err != nil {
				return fmt.Errorf("definition: folder %q: grantee_id must be a UUID", n.Name)
			}
		}
		for _, doc := range n.Docs {
			docs++
			if docs > TemplateMaxDocs {
				return fmt.Errorf("definition: more than %d placeholder documents", TemplateMaxDocs)
			}
			if strings.TrimSpace(doc.Title) == "" {
				return fmt.Errorf("definition: folder %q: document title required", n.Name)
			}
		}
		for i := range n.Children {
			if err := walk(&n.Children[i], depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for i := range d.Nodes {
		if err := walk(&d.Nodes[i], 1); err != nil {
			return err
		}
	}
	return nil
}

// templateVarRe matches {{ var_name }} references.
var templateVarRe = regexp.MustCompile(`\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}`)

// Variables returns the sorted set of variable names referenced
// anywhere in the definition (folder names, doc titles, metadata
// values) — the provision dialog prompts for exactly these.
func (d *TemplateDefinition) Variables() []string {
	seen := map[string]struct{}{}
	collect := func(s string) {
		for _, m := range templateVarRe.FindAllStringSubmatch(s, -1) {
			seen[m[1]] = struct{}{}
		}
	}
	var walk func(n *TemplateNode)
	walk = func(n *TemplateNode) {
		collect(n.Name)
		for _, v := range n.Metadata {
			collect(v)
		}
		for _, doc := range n.Docs {
			collect(doc.Title)
			for _, v := range doc.Metadata {
				collect(v)
			}
			for _, t := range doc.Tags {
				collect(t)
			}
		}
		for i := range n.Children {
			walk(&n.Children[i])
		}
	}
	for i := range d.Nodes {
		walk(&d.Nodes[i])
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// SubstituteTemplateVars replaces every {{var}} in s. Unknown variables
// are an error — a half-substituted "Project {{name}}" folder is worse
// than a rejected provision.
func SubstituteTemplateVars(s string, vars map[string]string) (string, error) {
	var missing []string
	out := templateVarRe.ReplaceAllStringFunc(s, func(m string) string {
		name := templateVarRe.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unresolved template variable(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// TemplateProvisionedPayload is the dms.template.provisioned.v1 event
// body (outbox-emitted in the provision tx).
type TemplateProvisionedPayload struct {
	TemplateID     string            `json:"template_id"`
	TemplateName   string            `json:"template_name"`
	WorkspaceID    string            `json:"workspace_id"`
	ParentFolderID string            `json:"parent_folder_id,omitempty"`
	RootFolderIDs  []string          `json:"root_folder_ids"`
	FoldersCreated int               `json:"folders_created"`
	DocsCreated    int               `json:"docs_created"`
	Variables      map[string]string `json:"variables,omitempty"`
	ProvisionedBy  string            `json:"provisioned_by"`
}
