package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Resolvers is the v1 read-side interface. Every Query field on
// the schema maps to one method here. Returning (nil, nil) is the
// resolver's way of saying "permission-denied or not found"; the
// executor renders that as a JSON null rather than an error so
// the rest of the response keeps assembling.
type Resolvers interface {
	Document(ctx context.Context, id string) (any, error)
	DocumentsByWorkspace(ctx context.Context, workspaceID string, limit int, cursor string) (any, error)

	WorkflowInstance(ctx context.Context, id string) (any, error)
	WorkflowInstancesByDocument(ctx context.Context, documentID string, limit int, cursor string) (any, error)

	TaskByID(ctx context.Context, id string) (any, error)
	MyTasks(ctx context.Context, includeCompleted bool, limit int, cursor string) (any, error)

	ActivityForDocument(ctx context.Context, documentID string, limit int, cursor string) (any, error)

	// Child resolvers — invoked when the query selects a field that
	// requires a fan-out into a different upstream service. Each
	// must return a value compatible with the parent type's field
	// (e.g. Versions → *model.Connection[*model.Version]).
	DocumentVersions(ctx context.Context, parent any, limit int, cursor string) (any, error)
	DocumentCurrentVersion(ctx context.Context, parent any) (any, error)
	DocumentComments(ctx context.Context, parent any, includeResolved bool, limit int, cursor string) (any, error)
	DocumentAnnotations(ctx context.Context, parent any, limit int, cursor string) (any, error)
	DocumentWorkflowInstances(ctx context.Context, parent any, limit int) (any, error)
	DocumentPermissions(ctx context.Context, parent any) (any, error)

	WorkflowInstanceTasks(ctx context.Context, parent any) (any, error)
	WorkflowInstanceDocument(ctx context.Context, parent any) (any, error)

	TaskWorkflowInstance(ctx context.Context, parent any) (any, error)

	CommentReplies(ctx context.Context, parent any) (any, error)
}

// Result is the standard GraphQL response envelope.
type Result struct {
	Data   map[string]any           `json:"data,omitempty"`
	Errors []*gqlerror.Error        `json:"errors,omitempty"`
}

// Executor parses + validates + dispatches a single GraphQL
// operation. Schema is shared across requests; resolvers and the
// per-request DataLoader cache live on the context.
type Executor struct {
	Schema    *ast.Schema
	Resolvers Resolvers
}

// Execute runs one operation. `query` is the document text;
// `variables` is the JSON variables map; `opName` selects the
// operation when the document contains more than one (empty =
// pick the only operation).
func (e *Executor) Execute(ctx context.Context, query string, variables map[string]any, opName string) *Result {
	doc, err := gqlparser.LoadQuery(e.Schema, query)
	if err != nil {
		return &Result{Errors: []*gqlerror.Error{gqlerror.Errorf("parse: %s", err.Error())}}
	}
	op := pickOperation(doc, opName)
	if op == nil {
		return &Result{Errors: []*gqlerror.Error{gqlerror.Errorf("operation %q not found", opName)}}
	}
	if op.Operation != ast.Query {
		return &Result{Errors: []*gqlerror.Error{gqlerror.Errorf("only query operations are supported")}}
	}
	out := map[string]any{}
	var errs []*gqlerror.Error
	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		val, err := e.resolveTopLevel(ctx, field, variables)
		alias := field.Alias
		if alias == "" {
			alias = field.Name
		}
		if err != nil {
			errs = append(errs, gqlerror.Errorf("%s: %s", field.Name, err.Error()))
			out[alias] = nil
			continue
		}
		out[alias] = e.serializeField(ctx, field, val)
	}
	return &Result{Data: out, Errors: errs}
}

func pickOperation(doc *ast.QueryDocument, name string) *ast.OperationDefinition {
	if name == "" {
		if len(doc.Operations) > 0 {
			return doc.Operations[0]
		}
		return nil
	}
	for _, op := range doc.Operations {
		if op.Name == name {
			return op
		}
	}
	return nil
}

func (e *Executor) resolveTopLevel(ctx context.Context, field *ast.Field, vars map[string]any) (any, error) {
	args := collectArgs(field, vars)
	switch field.Name {
	case "document":
		return e.Resolvers.Document(ctx, asString(args["id"]))
	case "documentsByWorkspace":
		return e.Resolvers.DocumentsByWorkspace(ctx, asString(args["workspaceId"]), asInt(args["limit"], 50), asString(args["cursor"]))
	case "workflowInstance":
		return e.Resolvers.WorkflowInstance(ctx, asString(args["id"]))
	case "workflowInstancesByDocument":
		return e.Resolvers.WorkflowInstancesByDocument(ctx, asString(args["documentId"]), asInt(args["limit"], 50), asString(args["cursor"]))
	case "taskById":
		return e.Resolvers.TaskByID(ctx, asString(args["id"]))
	case "myTasks":
		return e.Resolvers.MyTasks(ctx, asBool(args["includeCompleted"]), asInt(args["limit"], 50), asString(args["cursor"]))
	case "activityForDocument":
		return e.Resolvers.ActivityForDocument(ctx, asString(args["documentId"]), asInt(args["limit"], 50), asString(args["cursor"]))
	}
	return nil, fmt.Errorf("unknown root field %q", field.Name)
}

// serializeField walks a resolved value against the requested
// selection set, projecting only the fields the client asked for.
// Scalars marshal directly; objects recurse; lists recurse per
// element. Connection envelopes get their nodes recursed too.
func (e *Executor) serializeField(ctx context.Context, field *ast.Field, val any) any {
	if val == nil {
		return nil
	}
	// Slice (list of objects).
	rv := reflect.ValueOf(val)
	if rv.Kind() == reflect.Slice {
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = e.serializeField(ctx, field, rv.Index(i).Interface())
		}
		return out
	}
	// Leaf: no selection set → marshal as-is via JSON.
	if len(field.SelectionSet) == 0 {
		return marshalScalar(val)
	}
	// Object: project requested fields.
	return e.projectObject(ctx, field, val)
}

func (e *Executor) projectObject(ctx context.Context, parentField *ast.Field, val any) map[string]any {
	out := map[string]any{}
	parent := derefStruct(val)
	for _, sel := range parentField.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		alias := f.Alias
		if alias == "" {
			alias = f.Name
		}
		// Child resolvers — these run upstream calls (DataLoader-batched).
		args := collectArgs(f, nil)
		if child, handled := e.callChildResolver(ctx, parentField, f, val, args); handled {
			out[alias] = e.serializeField(ctx, f, child)
			continue
		}
		// Direct field: read from the struct via reflection on JSON tag.
		fv := lookupField(parent, f.Name)
		if !fv.IsValid() {
			out[alias] = nil
			continue
		}
		out[alias] = e.serializeField(ctx, f, fv.Interface())
	}
	return out
}

func (e *Executor) callChildResolver(ctx context.Context, parentField, f *ast.Field, parent any, args map[string]any) (any, bool) {
	// Map (parentTypeName, fieldName) → resolver method.
	parentType := parentField.Definition.Type.Name()
	switch parentType {
	case "Document":
		switch f.Name {
		case "currentVersion":
			v, err := e.Resolvers.DocumentCurrentVersion(ctx, parent)
			return resolverResult(v, err), true
		case "versions":
			v, err := e.Resolvers.DocumentVersions(ctx, parent, asInt(args["limit"], 50), asString(args["cursor"]))
			return resolverResult(v, err), true
		case "comments":
			v, err := e.Resolvers.DocumentComments(ctx, parent, asBool(args["includeResolved"]), asInt(args["limit"], 50), asString(args["cursor"]))
			return resolverResult(v, err), true
		case "annotations":
			v, err := e.Resolvers.DocumentAnnotations(ctx, parent, asInt(args["limit"], 50), asString(args["cursor"]))
			return resolverResult(v, err), true
		case "workflowInstances":
			v, err := e.Resolvers.DocumentWorkflowInstances(ctx, parent, asInt(args["limit"], 50))
			return resolverResult(v, err), true
		case "permissions":
			v, err := e.Resolvers.DocumentPermissions(ctx, parent)
			return resolverResult(v, err), true
		}
	case "WorkflowInstance":
		switch f.Name {
		case "tasks":
			v, err := e.Resolvers.WorkflowInstanceTasks(ctx, parent)
			return resolverResult(v, err), true
		case "document":
			v, err := e.Resolvers.WorkflowInstanceDocument(ctx, parent)
			return resolverResult(v, err), true
		}
	case "Task":
		if f.Name == "workflowInstance" {
			v, err := e.Resolvers.TaskWorkflowInstance(ctx, parent)
			return resolverResult(v, err), true
		}
	case "Comment":
		if f.Name == "replies" {
			v, err := e.Resolvers.CommentReplies(ctx, parent)
			return resolverResult(v, err), true
		}
	}
	return nil, false
}

func resolverResult(v any, err error) any {
	if err != nil {
		// Errors on child resolvers degrade to null rather than
		// failing the whole query. The caller gets a partial
		// response with the parent fields it could resolve.
		return nil
	}
	return v
}

// collectArgs flattens a field's args + applies variable
// substitution. gqlparser already coerces literal types per the
// schema; variables come in as the JSON-decoded map.
func collectArgs(field *ast.Field, vars map[string]any) map[string]any {
	out := map[string]any{}
	for _, a := range field.Arguments {
		v, _ := a.Value.Value(vars)
		out[a.Name] = v
	}
	return out
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func asInt(v any, def int) int {
	if v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func derefStruct(v any) reflect.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	return rv
}

// lookupField resolves a GraphQL field (camelCase) onto a struct
// field (matching `json` tag prefix). Falls back to a case-
// insensitive name match so connection envelopes (Nodes, NextCursor)
// still resolve.
func lookupField(rv reflect.Value, gqlName string) reflect.Value {
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return reflect.Value{}
	}
	t := rv.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		jsonName := tag
		if comma := indexOf(tag, ','); comma >= 0 {
			jsonName = tag[:comma]
		}
		if jsonName == gqlName {
			return rv.Field(i)
		}
		if equalFold(f.Name, gqlName) {
			return rv.Field(i)
		}
	}
	return reflect.Value{}
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func marshalScalar(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.Format(time.RFC3339Nano)
	}
	if t, ok := v.(*time.Time); ok {
		if t == nil {
			return nil
		}
		return t.Format(time.RFC3339Nano)
	}
	// Fall through: standard JSON marshaler handles primitives.
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}
