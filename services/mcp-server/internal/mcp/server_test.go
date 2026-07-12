package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
)

// okTool / errTool are minimal handlers for exercising the dispatcher
// without any upstream HTTP.
func okTool(text string) ToolFunc {
	return func(context.Context, json.RawMessage) (*ToolCallResult, error) {
		return &ToolCallResult{Content: []ContentBlock{{Type: "text", Text: text}}}, nil
	}
}
func errTool(err error) ToolFunc {
	return func(context.Context, json.RawMessage) (*ToolCallResult, error) { return nil, err }
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// newServer builds a Server whose scope reader pulls from ctx (as prod
// does via auth.GetScopes) and whose audit emitter records calls so tests
// can assert the "every call is audited" invariant.
func newServer(t *testing.T, tools ...ToolDef) (*Server, *[]auditRec) {
	t.Helper()
	var audits []auditRec
	s := New(Config{
		Logger:  zerolog.Nop(),
		ScopeOf: auth.GetScopes,
		AuditEmit: func(_ context.Context, tool string, ok bool, errMsg string) {
			audits = append(audits, auditRec{tool: tool, ok: ok, err: errMsg})
		},
	})
	for _, tl := range tools {
		s.Register(tl)
	}
	return s, &audits
}

type auditRec struct {
	tool string
	ok   bool
	err  string
}

func ctxWithScopes(scopes ...string) context.Context {
	return auth.WithScopes(context.Background(), scopes)
}

func TestDispatch_MethodRouting(t *testing.T) {
	s, _ := newServer(t, ToolDef{Name: "search_documents", Description: "d", Handler: okTool("hi")})

	tests := []struct {
		name       string
		method     string
		wantErr    bool
		wantCode   int
		assertResp func(t *testing.T, r Response)
	}{
		{
			name:   "initialize returns protocol version + tools capability",
			method: "initialize",
			assertResp: func(t *testing.T, r Response) {
				res, ok := r.Result.(InitializeResult)
				require.True(t, ok, "initialize result type")
				assert.Equal(t, ProtocolVersion, res.ProtocolVersion)
				assert.Contains(t, res.Capabilities, "tools")
			},
		},
		{
			name:   "tools/list enumerates registered tools with schema",
			method: "tools/list",
			assertResp: func(t *testing.T, r Response) {
				res, ok := r.Result.(ToolsListResult)
				require.True(t, ok)
				require.Len(t, res.Tools, 1)
				assert.Equal(t, "search_documents", res.Tools[0].Name)
			},
		},
		{name: "ping acknowledges", method: "ping"},
		{name: "initialized notification acknowledges", method: "notifications/initialized"},
		{name: "unknown method → -32601", method: "frobnicate", wantErr: true, wantCode: -32601},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.Dispatch(context.Background(), Request{
				JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: tc.method,
			})
			assert.Equal(t, "2.0", resp.JSONRPC)
			if tc.wantErr {
				require.NotNil(t, resp.Error)
				assert.Equal(t, tc.wantCode, resp.Error.Code)
				return
			}
			require.Nil(t, resp.Error, "unexpected error: %+v", resp.Error)
			if tc.assertResp != nil {
				tc.assertResp(t, resp)
			}
		})
	}
}

func TestToolsCall_DispatchAndErrorCodes(t *testing.T) {
	boom := errors.New("upstream 500: nope")
	srv, audits := newServer(t,
		ToolDef{Name: "search_documents", RequiredScope: "mcp:read", Handler: okTool("results")},
		ToolDef{Name: "upload_document", RequiredScope: "mcp:write", Handler: okTool("created")},
		ToolDef{Name: "flaky_tool", RequiredScope: "mcp:read", Handler: errTool(boom)},
	)

	tests := []struct {
		name       string
		ctxScopes  []string
		params     any             // marshalled into req.Params; raw overrides
		rawParams  json.RawMessage // when non-nil, used verbatim (malformed cases)
		wantErr    bool
		wantCode   int
		wantAudit  *auditRec
		wantResult string
	}{
		{
			name:       "known tool with sufficient scope succeeds + audits ok",
			ctxScopes:  []string{"mcp:read"},
			params:     ToolCallParams{Name: "search_documents", Arguments: json.RawMessage(`{"query":"x"}`)},
			wantResult: "results",
			wantAudit:  &auditRec{tool: "search_documents", ok: true},
		},
		{
			name:      "unknown tool → ErrCodeToolNotFound",
			ctxScopes: []string{"mcp:*"},
			params:    ToolCallParams{Name: "no_such_tool"},
			wantErr:   true, wantCode: ErrCodeToolNotFound,
			wantAudit: &auditRec{tool: "no_such_tool", ok: false, err: "tool not found"},
		},
		{
			name:      "write tool without mcp:write → ErrCodeMissingScope",
			ctxScopes: []string{"mcp:read"},
			params:    ToolCallParams{Name: "upload_document", Arguments: json.RawMessage(`{}`)},
			wantErr:   true, wantCode: ErrCodeMissingScope,
			wantAudit: &auditRec{tool: "upload_document", ok: false, err: "missing scope"},
		},
		{
			name:      "malformed params → ErrCodeInvalidArgument",
			ctxScopes: []string{"mcp:*"},
			rawParams: json.RawMessage(`"not-an-object"`),
			wantErr:   true, wantCode: ErrCodeInvalidArgument,
		},
		{
			name:      "handler error → ErrCodeToolExecFailed + audits failure",
			ctxScopes: []string{"mcp:read"},
			params:    ToolCallParams{Name: "flaky_tool", Arguments: json.RawMessage(`{}`)},
			wantErr:   true, wantCode: ErrCodeToolExecFailed,
			wantAudit: &auditRec{tool: "flaky_tool", ok: false, err: boom.Error()},
		},
		{
			name:       "mcp:* grants a write tool",
			ctxScopes:  []string{"mcp:*"},
			params:     ToolCallParams{Name: "upload_document", Arguments: json.RawMessage(`{}`)},
			wantResult: "created",
			wantAudit:  &auditRec{tool: "upload_document", ok: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			*audits = nil
			raw := tc.rawParams
			if raw == nil {
				raw = mustJSON(t, tc.params)
			}
			resp := srv.Dispatch(ctxWithScopes(tc.ctxScopes...), Request{
				JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "tools/call", Params: raw,
			})
			if tc.wantErr {
				require.NotNil(t, resp.Error, "want error code %d", tc.wantCode)
				assert.Equal(t, tc.wantCode, resp.Error.Code)
			} else {
				require.Nil(t, resp.Error, "unexpected error: %+v", resp.Error)
				res, ok := resp.Result.(*ToolCallResult)
				require.True(t, ok)
				require.Len(t, res.Content, 1)
				assert.Equal(t, tc.wantResult, res.Content[0].Text)
			}
			if tc.wantAudit != nil {
				require.Len(t, *audits, 1, "exactly one audit per call")
				got := (*audits)[0]
				assert.Equal(t, tc.wantAudit.tool, got.tool)
				assert.Equal(t, tc.wantAudit.ok, got.ok)
				if tc.wantAudit.err != "" {
					assert.Equal(t, tc.wantAudit.err, got.err)
				}
			}
		})
	}
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	s, _ := newServer(t)
	s.Register(ToolDef{Name: "dup", Handler: okTool("a")})
	assert.PanicsWithValue(t, "mcp: tool already registered: dup", func() {
		s.Register(ToolDef{Name: "dup", Handler: okTool("b")})
	})
}

func TestHasScope(t *testing.T) {
	tests := []struct {
		scopes []string
		want   string
		ok     bool
	}{
		{[]string{"mcp:read"}, "mcp:read", true},
		{[]string{"mcp:read"}, "mcp:write", false},
		{[]string{"mcp:*"}, "mcp:write", true},
		{[]string{"mcp:*"}, "mcp:read", true},
		{nil, "mcp:read", false},
		{[]string{"other:scope"}, "mcp:read", false},
	}
	for _, tc := range tests {
		assert.Equalf(t, tc.ok, hasScope(tc.scopes, tc.want), "scopes=%v want=%s", tc.scopes, tc.want)
	}
}

// A tool that requires no scope must dispatch even with an empty scope set
// (the route-boundary APIKeyAuth already gated mcp:read).
func TestToolsCall_NoRequiredScope_AllowsEmptyCtxScopes(t *testing.T) {
	s, _ := newServer(t, ToolDef{Name: "open_tool", Handler: okTool("ok")})
	resp := s.Dispatch(context.Background(), Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: mustJSON(t, ToolCallParams{Name: "open_tool", Arguments: json.RawMessage(`{}`)}),
	})
	require.Nil(t, resp.Error)
}
