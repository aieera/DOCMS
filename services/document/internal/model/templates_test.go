package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func validDef() json.RawMessage {
	return json.RawMessage(`{
		"nodes": [
			{
				"name": "Project {{project_name}}",
				"metadata": {"project": "{{project_name}}"},
				"docs": [{"title": "{{project_name}} charter", "tags": ["{{phase}}"]}],
				"children": [
					{"name": "Contracts", "grants": [{"grantee_type": "group", "grantee_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6"}]},
					{"name": "Deliverables", "visibility": "private"}
				]
			}
		]
	}`)
}

func TestParseTemplateDefinition_Valid(t *testing.T) {
	def, err := ParseTemplateDefinition(validDef())
	if err != nil {
		t.Fatalf("valid definition rejected: %v", err)
	}
	if len(def.Nodes) != 1 || len(def.Nodes[0].Children) != 2 {
		t.Fatalf("unexpected parse shape: %+v", def)
	}
}

func TestParseTemplateDefinition_Rejections(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"empty nodes", `{"nodes": []}`, "at least one root"},
		{"missing name", `{"nodes": [{"name": "  "}]}`, "name required"},
		{"bad visibility", `{"nodes": [{"name": "x", "visibility": "secret"}]}`, "visibility"},
		{"bad grantee type", `{"nodes": [{"name": "x", "grants": [{"grantee_type": "role", "grantee_id": "3fa85f64-5717-4562-b3fc-2c963f66afa6"}]}]}`, "grantee_type"},
		{"bad grantee id", `{"nodes": [{"name": "x", "grants": [{"grantee_type": "user", "grantee_id": "nope"}]}]}`, "grantee_id"},
		{"doc without title", `{"nodes": [{"name": "x", "docs": [{"title": ""}]}]}`, "document title"},
		{"unknown field", `{"nodes": [{"name": "x", "colour": "red"}]}`, "definition"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseTemplateDefinition(json.RawMessage(c.raw))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestParseTemplateDefinition_DepthCap(t *testing.T) {
	// Build a chain deeper than TemplateMaxDepth.
	inner := `{"name": "leaf"}`
	for i := 0; i < TemplateMaxDepth+1; i++ {
		inner = `{"name": "n", "children": [` + inner + `]}`
	}
	_, err := ParseTemplateDefinition(json.RawMessage(`{"nodes": [` + inner + `]}`))
	if err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("want depth error, got %v", err)
	}
}

func TestVariables_ExtractsSortedUniqueSet(t *testing.T) {
	def, err := ParseTemplateDefinition(validDef())
	if err != nil {
		t.Fatal(err)
	}
	vars := def.Variables()
	if len(vars) != 2 || vars[0] != "phase" || vars[1] != "project_name" {
		t.Fatalf("want [phase project_name], got %v", vars)
	}
}

func TestSubstituteTemplateVars(t *testing.T) {
	out, err := SubstituteTemplateVars("Project {{ project_name }} ({{phase}})",
		map[string]string{"project_name": "Apollo", "phase": "P1"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Project Apollo (P1)" {
		t.Fatalf("got %q", out)
	}

	if _, err := SubstituteTemplateVars("{{missing}}", nil); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Fatalf("unresolved variable must error, got %v", err)
	}

	// No variables → passthrough.
	out, err = SubstituteTemplateVars("plain", map[string]string{"unused": "x"})
	if err != nil || out != "plain" {
		t.Fatalf("passthrough failed: %q %v", out, err)
	}
}
