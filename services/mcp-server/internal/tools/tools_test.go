package tools_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
	"github.com/aieera/sedoc/services/mcp-server/internal/tools"
)

// validArgs holds a well-formed argument set per registered tool. It
// doubles as a coverage guard: TestTools_EveryToolPropagatesIdentity
// fails if tools.All grows a tool with no entry here, so a new
// LLM-facing tool cannot ship without an identity-propagation assertion.
var validArgs = map[string]map[string]any{
	"search_documents": {"query": "quarterly report"},
	"get_document":     {"document_id": uuid.NewString()},
	"upload_document":  {"workspace_id": uuid.NewString(), "title": "t", "content": "hello"},
	"start_workflow":   {"definition_id": uuid.NewString(), "document_id": uuid.NewString()},
}

type capturedReq struct {
	method string
	path   string
	header http.Header
	body   []byte
	hits   int
}

// fakeUpstream records the last inbound request and replies with a canned
// body — standing in for search/document/workflow so the test isolates
// the MCP tool layer (identity headers, validation, error mapping).
func fakeUpstream(t testing.TB, status int, respBody string) (string, *capturedReq) {
	t.Helper()
	cap := &capturedReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.method, cap.path, cap.header, cap.body = r.Method, r.URL.Path, r.Header.Clone(), b
		cap.hits++
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, cap
}

func cfgFor(base string) tools.Config {
	return tools.Config{
		SearchBaseURL:       base,
		DocumentBaseURL:     base,
		WorkflowBaseURL:     base,
		IntelligenceBaseURL: base,
		GatewaySecret:       "test-gateway-secret",
		HTTP:                &http.Client{Timeout: 5 * time.Second},
	}
}

func handlerByName(t *testing.T, defs []mcp.ToolDef, name string) mcp.ToolFunc {
	t.Helper()
	for _, d := range defs {
		if d.Name == name {
			return d.Handler
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}

func tenantCtx(tenant, user uuid.UUID) context.Context {
	return auth.WithUser(context.Background(), auth.UserInfo{ID: user, TenantID: tenant})
}

func argsJSON(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

// EVERY registered tool must stamp the caller's ctx tenant + user onto the
// upstream request — this is the entire tenant-scoping contract of the MCP
// proxy. Data-driven over tools.All so a new tool can't skip it.
func TestTools_EveryToolPropagatesIdentity(t *testing.T) {
	base, cap := fakeUpstream(t, 200, `{"ok":true}`)
	defs := tools.All(cfgFor(base))

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())

	require.NotEmpty(t, defs)
	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			args, ok := validArgs[d.Name]
			require.Truef(t, ok, "no validArgs entry for registered tool %q — add one so its tenant scoping is verified", d.Name)
			cap.hits = 0

			_, err := d.Handler(tenantCtx(tenant, user), argsJSON(t, args))
			require.NoError(t, err)
			require.Equal(t, 1, cap.hits, "tool must call exactly one upstream")

			assert.Equal(t, tenant.String(), cap.header.Get("X-Auth-Tenant-ID"),
				"tool must forward the ctx tenant, never a default or empty")
			assert.Equal(t, user.String(), cap.header.Get("X-User-ID"))
			assert.Equal(t, "test-gateway-secret", cap.header.Get("X-Gateway-Signature"),
				"internal calls must be gateway-signed")
			assert.Equal(t, "application/json", cap.header.Get("Content-Type"))
		})
	}
}

// Arguments must never be able to override the tenant: even when the caller
// injects a tenant_id/X-Auth-Tenant-ID into the args, the stamped upstream
// tenant stays the ctx (API-key) tenant. This is the anti-exfiltration pin.
func TestTools_ArgsCannotOverrideTenant(t *testing.T) {
	base, cap := fakeUpstream(t, 200, `{"ok":true}`)
	defs := tools.All(cfgFor(base))

	ctxTenant := uuid.Must(uuid.NewV7())
	attackerTenant := uuid.Must(uuid.NewV7())

	for _, d := range defs {
		t.Run(d.Name, func(t *testing.T) {
			args := map[string]any{}
			for k, v := range validArgs[d.Name] {
				args[k] = v
			}
			// Adversarial extras that a naive impl might trust:
			args["tenant_id"] = attackerTenant.String()
			args["X-Auth-Tenant-ID"] = attackerTenant.String()
			args["tenantId"] = attackerTenant.String()

			_, err := d.Handler(tenantCtx(ctxTenant, uuid.Must(uuid.NewV7())), argsJSON(t, args))
			require.NoError(t, err)
			assert.Equal(t, ctxTenant.String(), cap.header.Get("X-Auth-Tenant-ID"),
				"args must NOT be able to redirect the tenant — data-exfiltration guard")
			assert.NotEqual(t, attackerTenant.String(), cap.header.Get("X-Auth-Tenant-ID"))
		})
	}
}

func TestTools_ArgValidation_RejectsBeforeUpstream(t *testing.T) {
	base, cap := fakeUpstream(t, 200, `{}`)
	defs := tools.All(cfgFor(base))
	ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))

	tests := []struct {
		tool string
		raw  string
	}{
		{"search_documents", `{}`},                                // missing query
		{"search_documents", `{"query":123}`},                     // wrong type
		{"search_documents", `{not json`},                         // malformed
		{"get_document", `{}`},                                    // missing document_id
		{"get_document", `{"document_id":""}`},                    // empty document_id
		{"upload_document", `{"title":"t","content":"c"}`},        // missing workspace_id
		{"upload_document", `{"workspace_id":"w","content":"c"}`}, // missing title
		{"start_workflow", `{"document_id":"d"}`},                 // missing definition_id
		{"start_workflow", `{}`},                                  // missing both
	}
	for _, tc := range tests {
		t.Run(tc.tool+" "+tc.raw, func(t *testing.T) {
			cap.hits = 0
			_, err := handlerByName(t, defs, tc.tool)(ctx, json.RawMessage(tc.raw))
			require.Error(t, err, "invalid args must be rejected")
			assert.Equal(t, 0, cap.hits, "invalid args must NOT reach the upstream service")
		})
	}
}

func TestTools_UpstreamErrorMapped(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		base, _ := fakeUpstream(t, status, `{"error":"boom"}`)
		defs := tools.All(cfgFor(base))
		ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
		_, err := handlerByName(t, defs, "get_document")(ctx, argsJSON(t, validArgs["get_document"]))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upstream")
	}
}

// A hostile/huge upstream body must be bounded so a tool can't blow up the
// client (the do() helper wraps the body in a 1 MiB LimitReader).
func TestTools_OutputSizeIsBounded(t *testing.T) {
	huge := strings.Repeat("A", 4<<20) // 4 MiB
	base, _ := fakeUpstream(t, 200, huge)
	defs := tools.All(cfgFor(base))
	ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))

	res, err := handlerByName(t, defs, "search_documents")(ctx, argsJSON(t, validArgs["search_documents"]))
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.LessOrEqual(t, len(res.Content[0].Text), 1<<20,
		"tool output must be capped at 1 MiB regardless of upstream size")
}

func TestUploadDocument_RejectsOversizeContentBeforeUpstream(t *testing.T) {
	base, cap := fakeUpstream(t, 200, `{}`)
	defs := tools.All(cfgFor(base))
	ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))

	args := map[string]any{
		"workspace_id": uuid.NewString(),
		"title":        "big",
		"content":      strings.Repeat("x", 256*1024+1), // one byte over the cap
	}
	_, err := handlerByName(t, defs, "upload_document")(ctx, argsJSON(t, args))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
	assert.Equal(t, 0, cap.hits, "oversize upload must be rejected before the upstream write")
}

func TestSearchDocuments_AppliesDefaultLimit(t *testing.T) {
	base, cap := fakeUpstream(t, 200, `{}`)
	defs := tools.All(cfgFor(base))
	ctx := tenantCtx(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))

	_, err := handlerByName(t, defs, "search_documents")(ctx, json.RawMessage(`{"query":"x"}`))
	require.NoError(t, err)
	var sent map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &sent))
	assert.EqualValues(t, 10, sent["limit"], "omitted limit must default to 10")
	assert.Equal(t, "x", sent["q"])
}
