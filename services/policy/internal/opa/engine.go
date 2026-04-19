// Package opa wraps the embedded Rego policy compiled at startup. One
// Engine is constructed per process and shared across all goroutines —
// OPA's PreparedEvalQuery is concurrency-safe.
package opa

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/rego"
	"github.com/open-policy-agent/opa/storage/inmem"

	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

//go:embed policy.rego
var policySrc string

// Engine is a compiled Rego evaluator. Construct once via New; reuse.
// The policy is compiled once at startup; each Eval call constructs a
// fresh rego.New() with the per-call data store (OPA's prepared query
// API does not support per-call data stores).
type Engine struct {
	compiler *ast.Compiler
}

// New parses + compiles the embedded policy.rego. Errors indicate a
// policy syntax / compilation problem and should crash the service on
// startup.
func New(_ context.Context) (*Engine, error) {
	module, err := ast.ParseModule("policy.rego", policySrc)
	if err != nil {
		return nil, fmt.Errorf("parse policy.rego: %w", err)
	}
	compiler := ast.NewCompiler()
	compiler.Compile(map[string]*ast.Module{"policy.rego": module})
	if compiler.Failed() {
		return nil, fmt.Errorf("compile policy: %w", compiler.Errors)
	}
	return &Engine{compiler: compiler}, nil
}

// EvalInput bundles input + data passed to a single evaluation.
type EvalInput struct {
	Input       model.CheckInput
	Permissions []PermissionDoc
	UserGroups  []string
	Workspaces  []WorkspaceDoc
}

// PermissionDoc is the Rego-facing shape; strings and RFC3339 times so the
// embedded policy can manipulate them without type gymnastics.
type PermissionDoc struct {
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Capability    string `json:"capability"`
	ValidTo       string `json:"valid_to,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

// WorkspaceDoc is the subset of workspace_members the policy uses.
type WorkspaceDoc struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
}

// Eval runs the prepared query against in. Returns "allow" | "deny" as a
// string and the nanosecond duration of the Rego call (useful for p99
// metrics). The caller maps allow → true/false.
func (e *Engine) Eval(ctx context.Context, in EvalInput) (model.CheckResult, time.Duration, error) {
	data := map[string]any{
		"permissions":       toPermissionsJSON(in.Permissions),
		"user_groups":       in.UserGroups,
		"workspace_members": toWorkspacesJSON(in.Workspaces),
	}
	store := inmem.NewFromObject(data)

	start := time.Now()
	rs, err := rego.New(
		rego.Query("data.vaultdms.authz.final_decision"),
		rego.Compiler(e.compiler),
		rego.Store(store),
		rego.Input(in.Input),
	).Eval(ctx)
	elapsed := time.Since(start)
	if err != nil {
		return model.CheckResult{}, elapsed, fmt.Errorf("rego eval: %w", err)
	}
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return model.CheckResult{Allowed: false, Reason: "no policy decision"}, elapsed, nil
	}
	decision, _ := rs[0].Expressions[0].Value.(string)
	if decision == "allow" {
		return model.CheckResult{Allowed: true}, elapsed, nil
	}
	return model.CheckResult{Allowed: false, Reason: "policy denied"}, elapsed, nil
}

// toPermissionsJSON converts the Go slice into the []any Rego expects.
func toPermissionsJSON(in []PermissionDoc) []any {
	out := make([]any, 0, len(in))
	for _, p := range in {
		out = append(out, map[string]any{
			"resource_type":  p.ResourceType,
			"resource_id":    p.ResourceID,
			"principal_type": p.PrincipalType,
			"principal_id":   p.PrincipalID,
			"capability":     p.Capability,
			"valid_to":       p.ValidTo,
			"expires_at":     p.ExpiresAt,
		})
	}
	return out
}

func toWorkspacesJSON(in []WorkspaceDoc) []any {
	out := make([]any, 0, len(in))
	for _, w := range in {
		out = append(out, map[string]any{
			"user_id":      w.UserID,
			"workspace_id": w.WorkspaceID,
			"role":         w.Role,
		})
	}
	return out
}
