package opensearch

import (
	"encoding/json"
	"testing"
)

func TestTemplateJSON_IsValidJSON(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(TemplateJSON), &parsed); err != nil {
		t.Fatalf("TemplateJSON is not valid JSON: %v", err)
	}
	if _, ok := parsed["template"]; !ok {
		t.Fatal("TemplateJSON missing 'template' key")
	}
}

func TestTemplateJSON_HasReadableByField(t *testing.T) {
	var parsed map[string]any
	_ = json.Unmarshal([]byte(TemplateJSON), &parsed)

	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	props := mappings["properties"].(map[string]any)

	if _, ok := props["readable_by"]; !ok {
		t.Fatal("readable_by field missing from mapping — security filter would fail")
	}
	rb := props["readable_by"].(map[string]any)
	if rb["type"] != "keyword" {
		t.Fatalf("readable_by must be keyword for terms filter, got %v", rb["type"])
	}
}

func TestTemplateJSON_HasAutocompleteAnalyzer(t *testing.T) {
	var parsed map[string]any
	_ = json.Unmarshal([]byte(TemplateJSON), &parsed)

	tmpl := parsed["template"].(map[string]any)
	settings := tmpl["settings"].(map[string]any)
	analysis, ok := settings["analysis"].(map[string]any)
	if !ok {
		t.Fatal("analysis block missing from settings")
	}
	analyzers := analysis["analyzer"].(map[string]any)
	if _, ok := analyzers["autocomplete_analyzer"]; !ok {
		t.Fatal("autocomplete_analyzer missing")
	}
}

func TestTemplateJSON_StrictDynamic(t *testing.T) {
	var parsed map[string]any
	_ = json.Unmarshal([]byte(TemplateJSON), &parsed)

	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	if mappings["dynamic"] != "strict" {
		t.Fatal("mapping should be dynamic:strict to prevent arbitrary field injection")
	}
}
