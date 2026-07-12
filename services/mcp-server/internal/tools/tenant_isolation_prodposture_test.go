//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture tenant isolation for the MCP tool surface (ADR 0091).
//
// The MCP server is an LLM-facing HTTP proxy: every tool forwards the
// caller's identity (X-Auth-Tenant-ID from the API key on ctx) to an
// upstream service, which enforces RLS. The exfiltration risk is a tool
// that forwards the wrong tenant — or forwards nothing and lets the
// upstream fall back to a bypass. This test proves, over the ENFORCED
// NOBYPASSRLS posture and REAL RLS repos, that a tenant-A MCP session can
// never read or write tenant-B data — for EVERY registered tool.
//
// The "upstream" here is a minimal but faithful RLS repo: it reads the
// X-Auth-Tenant-ID the tool stamps and runs the query inside
// database.WithTenantTx over a dms_app (NOBYPASSRLS) pool — exactly the
// production tenant-tx machinery. Booting the real search/document/workflow
// services (their full dependency trees) into this module is infeasible;
// what matters for THIS service is that its identity propagation, fed into
// real RLS, isolates tenants. It does.
package tools_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/mcp-server/internal/handler"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
	"github.com/aieera/sedoc/services/mcp-server/internal/tools"
)

const isolationDDL = `
CREATE TABLE IF NOT EXISTS mcp_documents (
    tenant_id UUID NOT NULL,
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title     TEXT NOT NULL,
    content   TEXT NOT NULL
);
ALTER TABLE mcp_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE mcp_documents FORCE  ROW LEVEL SECURITY;
CREATE POLICY mcp_documents_isolation ON mcp_documents
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE IF NOT EXISTS mcp_workflow_instances (
    tenant_id     UUID NOT NULL,
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    definition_id UUID NOT NULL,
    document_id   UUID NOT NULL
);
ALTER TABLE mcp_workflow_instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE mcp_workflow_instances FORCE  ROW LEVEL SECURITY;
CREATE POLICY mcp_wf_isolation ON mcp_workflow_instances
    USING (tenant_id = current_setting('app.current_tenant', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
`

func TestProdPosture_MCPToolTenantIsolation(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, isolationDDL)
	require.NoError(t, err, "create RLS fixture tables")
	// Tables created after NewProdPostureDB's initial grant need the grant too.
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;`)
	require.NoError(t, err)

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	userA := uuid.Must(uuid.NewV7())
	docA := uuid.Must(uuid.NewV7())
	docB := uuid.Must(uuid.NewV7())
	const secretA = "A-SECRET-quarterly-numbers"
	const secretB = "B-SECRET-should-never-leak"

	// Seed cross-tenant fixtures out-of-band (superuser bypasses RLS, like ops).
	for _, r := range []struct {
		tenant, id uuid.UUID
		content    string
	}{{tenantA, docA, secretA}, {tenantB, docB, secretB}} {
		_, err = db.Super.Exec(ctx,
			`INSERT INTO mcp_documents (tenant_id, id, title, content) VALUES ($1,$2,$3,$4)`,
			r.tenant, r.id, "doc", r.content)
		require.NoError(t, err)
	}

	// ---- REAL RLS upstream ---------------------------------------------
	withTenant := func(w http.ResponseWriter, r *http.Request, fn func(pgx.Tx) (int, any)) {
		tid, perr := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
		if perr != nil {
			http.Error(w, `{"error":"missing tenant"}`, http.StatusUnauthorized)
			return
		}
		var status int
		var payload any
		terr := database.WithTenantTx(r.Context(), db.App, tid, func(tx pgx.Tx) error {
			status, payload = fn(tx)
			return nil
		})
		if terr != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, terr.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}

	mux := http.NewServeMux()
	// search_documents → returns every document RLS lets this tenant see.
	mux.HandleFunc("POST /api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		withTenant(w, r, func(tx pgx.Tx) (int, any) {
			rows, _ := tx.Query(r.Context(), `SELECT id, title, content FROM mcp_documents`)
			defer rows.Close()
			var out []map[string]any
			for rows.Next() {
				var id uuid.UUID
				var title, content string
				_ = rows.Scan(&id, &title, &content)
				out = append(out, map[string]any{"id": id, "title": title, "content": content})
			}
			return http.StatusOK, map[string]any{"results": out}
		})
	})
	// get_document → 404 when RLS hides the row.
	mux.HandleFunc("GET /api/v1/documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		withTenant(w, r, func(tx pgx.Tx) (int, any) {
			id := r.PathValue("id")
			var title, content string
			err := tx.QueryRow(r.Context(),
				`SELECT title, content FROM mcp_documents WHERE id = $1`, id).Scan(&title, &content)
			if err != nil {
				return http.StatusNotFound, map[string]any{"error": "not found"}
			}
			return http.StatusOK, map[string]any{"id": id, "title": title, "content": content}
		})
	})
	// upload_document → INSERT under the caller's tenant (WITH CHECK enforced).
	mux.HandleFunc("POST /api/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		withTenant(w, r, func(tx pgx.Tx) (int, any) {
			var id uuid.UUID
			tid, _ := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
			err := tx.QueryRow(r.Context(),
				`INSERT INTO mcp_documents (tenant_id, title, content) VALUES ($1,$2,$3) RETURNING id`,
				tid, body["title"], body["description"]).Scan(&id)
			if err != nil {
				return http.StatusBadRequest, map[string]any{"error": err.Error()}
			}
			return http.StatusCreated, map[string]any{"id": id}
		})
	})
	// start_workflow → INSERT a workflow instance under the caller's tenant.
	mux.HandleFunc("POST /api/v1/workflows/instances", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		withTenant(w, r, func(tx pgx.Tx) (int, any) {
			var id uuid.UUID
			tid, _ := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
			err := tx.QueryRow(r.Context(),
				`INSERT INTO mcp_workflow_instances (tenant_id, definition_id, document_id)
				 VALUES ($1,$2,$3) RETURNING id`,
				tid, body["definition_id"], body["document_id"]).Scan(&id)
			if err != nil {
				return http.StatusBadRequest, map[string]any{"error": err.Error()}
			}
			return http.StatusCreated, map[string]any{"id": id}
		})
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	// ---- MCP server wired at the real upstream -------------------------
	srv := mcp.New(mcp.Config{Logger: zerolog.Nop(), ScopeOf: auth.GetScopes})
	cfg := tools.Config{
		SearchBaseURL: upstream.URL, DocumentBaseURL: upstream.URL,
		WorkflowBaseURL: upstream.URL, IntelligenceBaseURL: upstream.URL,
		GatewaySecret: "test-gateway-secret", HTTP: upstream.Client(),
	}
	registered := tools.All(cfg)
	for _, d := range registered {
		srv.Register(d)
	}

	ctxA := auth.WithScopes(auth.WithUser(ctx, auth.UserInfo{ID: userA, TenantID: tenantA}), []string{"mcp:*"})

	call := func(name string, args map[string]any) mcp.Response {
		raw, _ := json.Marshal(args)
		params, _ := json.Marshal(mcp.ToolCallParams{Name: name, Arguments: raw})
		return srv.Dispatch(ctxA, mcp.Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: params})
	}
	textOf := func(t *testing.T, resp mcp.Response) string {
		t.Helper()
		require.Nil(t, resp.Error, "unexpected error: %+v", resp.Error)
		res, ok := resp.Result.(*mcp.ToolCallResult)
		require.True(t, ok)
		require.NotEmpty(t, res.Content)
		return res.Content[0].Text
	}

	covered := map[string]bool{}

	t.Run("search_documents", func(t *testing.T) {
		covered["search_documents"] = true
		out := textOf(t, call("search_documents", map[string]any{"query": "SECRET"}))
		assert.Contains(t, out, secretA, "tenant A must see its own document")
		assert.NotContains(t, out, secretB, "tenant A must NOT see tenant B's document via search")
	})

	t.Run("get_document", func(t *testing.T) {
		covered["get_document"] = true
		// Own doc: allowed.
		own := textOf(t, call("get_document", map[string]any{"document_id": docA.String()}))
		assert.Contains(t, own, secretA)
		// Tenant B's doc by exact id: RLS hides it → upstream 404 → tool error,
		// and B's secret must not appear anywhere in the response.
		resp := call("get_document", map[string]any{"document_id": docB.String()})
		require.NotNil(t, resp.Error, "reading another tenant's document must fail")
		assert.NotContains(t, resp.Error.Message, secretB)
	})

	t.Run("upload_document", func(t *testing.T) {
		covered["upload_document"] = true
		out := textOf(t, call("upload_document", map[string]any{
			"workspace_id": uuid.NewString(), "title": "from-A", "content": "A-UPLOAD",
		}))
		var created struct {
			ID uuid.UUID `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &created))
		// The write must be attributed to tenant A (verify out-of-band).
		var gotTenant uuid.UUID
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT tenant_id FROM mcp_documents WHERE id = $1`, created.ID).Scan(&gotTenant))
		assert.Equal(t, tenantA, gotTenant, "MCP write must land under the caller's tenant, not another")
	})

	t.Run("start_workflow", func(t *testing.T) {
		covered["start_workflow"] = true
		out := textOf(t, call("start_workflow", map[string]any{
			"definition_id": uuid.NewString(), "document_id": docA.String(),
		}))
		var created struct {
			ID uuid.UUID `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &created))
		var gotTenant uuid.UUID
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT tenant_id FROM mcp_workflow_instances WHERE id = $1`, created.ID).Scan(&gotTenant))
		assert.Equal(t, tenantA, gotTenant, "workflow start must land under the caller's tenant")
	})

	// Coverage guard: EVERY registered tool must have a tenant-isolation
	// subtest above. A new LLM-facing tool cannot ship untested.
	for _, d := range registered {
		assert.Truef(t, covered[d.Name],
			"registered tool %q has no prod-posture tenant-isolation subtest", d.Name)
	}
}

// TestProdPosture_MCPTransportAuthn pins the transport gate main.go wires:
// the MCP endpoints are wrapped by middleware.APIKeyAuth(mcp:read). An
// unauthenticated caller must never reach the dispatcher, and a key lacking
// the scope is refused — verified against a real api_keys lookup on the
// enforced NOBYPASSRLS pool (api_keys is a global, non-RLS lookup table, so
// the pre-tenant lookup must still succeed under dms_app).
func TestProdPosture_MCPTransportAuthn(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS api_keys (
			id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id    UUID NOT NULL,
			user_id      UUID NOT NULL,
			key_hash     TEXT UNIQUE NOT NULL,
			scopes       TEXT[] NOT NULL DEFAULT '{}',
			expires_at   TIMESTAMPTZ,
			revoked_at   TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ
		);
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;`)
	require.NoError(t, err)

	seedKey := func(token string, scopes []string) {
		sum := sha256.Sum256([]byte(token))
		_, e := db.Super.Exec(ctx,
			`INSERT INTO api_keys (tenant_id, user_id, key_hash, scopes) VALUES ($1,$2,$3,$4)`,
			uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), hex.EncodeToString(sum[:]), scopes)
		require.NoError(t, e)
	}
	const readKey = "vdms_read_key_123"
	const noScopeKey = "vdms_noscope_key_456"
	seedKey(readKey, []string{"mcp:read"})
	seedKey(noScopeKey, []string{"other:scope"})

	// The exact gate from main.go: APIKeyAuth(mcp:read) around the handler.
	srv := mcp.New(mcp.Config{Logger: zerolog.Nop(), ScopeOf: auth.GetScopes})
	mux := http.NewServeMux()
	handler.New(srv, zerolog.Nop()).Register(mux)
	gated := middleware.APIKeyAuth(middleware.APIKeyAuthConfig{Pool: db.App, RequiredScope: "mcp:read"})(mux)
	ts := httptest.NewServer(gated)
	t.Cleanup(ts.Close)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	post := func(authz string) int {
		req, _ := http.NewRequestWithContext(ctx, "POST", ts.URL+"/api/v1/mcp", strings.NewReader(initBody))
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, e := ts.Client().Do(req)
		require.NoError(t, e)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	tests := []struct {
		name  string
		authz string
		want  int
	}{
		{"no api key → 401", "", http.StatusUnauthorized},
		{"non-vdms bearer → 401", "Bearer abc123", http.StatusUnauthorized},
		{"unknown key → 401", "Bearer vdms_unknown", http.StatusUnauthorized},
		{"valid key missing mcp:read scope → 403", "Bearer " + noScopeKey, http.StatusForbidden},
		{"valid mcp:read key → dispatched (200)", "Bearer " + readKey, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, post(tc.authz))
		})
	}
}
