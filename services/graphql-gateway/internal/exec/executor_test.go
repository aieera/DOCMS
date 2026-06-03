package exec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aieera/sedoc/services/graphql-gateway/internal/model"
)

// stubResolvers is a minimal Resolvers implementation for the
// executor smoke test. Only Document() returns a populated value;
// every other resolver returns nil so the executor's null-on-deny
// path is exercised.
type stubResolvers struct{}

func (stubResolvers) Document(_ context.Context, id string) (any, error) {
	if id != "doc-1" {
		return nil, nil
	}
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	return &model.Document{
		ID:             "doc-1",
		TenantID:       "t-1",
		WorkspaceID:    "w-1",
		Title:          "Smoke Test",
		LifecycleState: "ACTIVE",
		RegionPin:      "EU_WEST_1",
		Tags:           []string{"smoke", "test"},
		CreatedAt:      &now,
	}, nil
}
func (stubResolvers) DocumentsByWorkspace(context.Context, string, int, string) (any, error) {
	return nil, nil
}
func (stubResolvers) WorkflowInstance(context.Context, string) (any, error)   { return nil, nil }
func (stubResolvers) WorkflowInstancesByDocument(context.Context, string, int, string) (any, error) {
	return nil, nil
}
func (stubResolvers) TaskByID(context.Context, string) (any, error)              { return nil, nil }
func (stubResolvers) MyTasks(context.Context, bool, int, string) (any, error)    { return nil, nil }
func (stubResolvers) ActivityForDocument(context.Context, string, int, string) (any, error) {
	return nil, nil
}
func (stubResolvers) DocumentVersions(context.Context, any, int, string) (any, error) { return nil, nil }
func (stubResolvers) DocumentCurrentVersion(context.Context, any) (any, error)        { return nil, nil }
func (stubResolvers) DocumentComments(context.Context, any, bool, int, string) (any, error) {
	return nil, nil
}
func (stubResolvers) DocumentAnnotations(context.Context, any, int, string) (any, error) {
	return nil, nil
}
func (stubResolvers) DocumentWorkflowInstances(context.Context, any, int) (any, error) {
	return nil, nil
}
func (stubResolvers) DocumentPermissions(context.Context, any) (any, error) {
	return &model.DocumentPermissions{CanView: true, CanEdit: false}, nil
}
func (stubResolvers) WorkflowInstanceTasks(context.Context, any) (any, error)    { return nil, nil }
func (stubResolvers) WorkflowInstanceDocument(context.Context, any) (any, error) { return nil, nil }
func (stubResolvers) TaskWorkflowInstance(context.Context, any) (any, error)     { return nil, nil }
func (stubResolvers) CommentReplies(context.Context, any) (any, error)           { return nil, nil }

func TestExecutor_DocumentReturnsScalars(t *testing.T) {
	e := &Executor{Schema: MustLoadSchema(), Resolvers: stubResolvers{}}
	q := `query Doc { document(id: "doc-1") { id title lifecycleState tags permissions { canView canEdit } } }`
	res := e.Execute(context.Background(), q, nil, "")
	if len(res.Errors) > 0 {
		t.Fatalf("expected no errors, got %v", res.Errors)
	}
	b, _ := json.Marshal(res.Data)
	got := string(b)
	for _, want := range []string{
		`"id":"doc-1"`,
		`"title":"Smoke Test"`,
		`"lifecycleState":"ACTIVE"`,
		`"tags":["smoke","test"]`,
		`"canView":true`,
		`"canEdit":false`,
	} {
		if !contains(got, want) {
			t.Errorf("response missing %q\nfull: %s", want, got)
		}
	}
}

func TestExecutor_NullableDocumentDeniedReturnsNull(t *testing.T) {
	e := &Executor{Schema: MustLoadSchema(), Resolvers: stubResolvers{}}
	q := `query Doc { document(id: "missing") { id } }`
	res := e.Execute(context.Background(), q, nil, "")
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if res.Data["document"] != nil {
		t.Fatalf("expected null for missing document, got %v", res.Data["document"])
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOfSubstr(s, sub) >= 0)
}

func indexOfSubstr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
