package tools_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/mcp-server/internal/tools"
)

// C-6: the workflow API honors X-Auth-Tenant-ID / X-User-* only together
// with a valid X-Internal-Service-Key (DEC-3). The MCP server's workflow call
// must carry the internal key, the gateway signature and the MCP caller's
// identity — including an explicit role, because a role-less internal call
// defaults to admin on the receiver.
func TestMCPWorkflowCall_SendsInternalKey(t *testing.T) {
	base, cap := fakeUpstream(t, 201, `{"id":"wf-1"}`)
	cfg := cfgFor(base)
	cfg.InternalKey = "test-internal-key"
	defs := tools.All(cfg)

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	ctx := auth.WithUser(context.Background(), auth.UserInfo{ID: user, TenantID: tenant, Role: "api_key"})

	_, err := handlerByName(t, defs, "start_workflow")(ctx, argsJSON(t, validArgs["start_workflow"]))
	require.NoError(t, err)
	require.Equal(t, 1, cap.hits)
	assert.Equal(t, "/api/v1/workflows/instances", cap.path)
	assert.Equal(t, "test-internal-key", cap.header.Get("X-Internal-Service-Key"))
	assert.Equal(t, "test-gateway-secret", cap.header.Get("X-Gateway-Signature"))
	assert.Equal(t, tenant.String(), cap.header.Get("X-Auth-Tenant-ID"))
	assert.Equal(t, user.String(), cap.header.Get("X-User-ID"))
	assert.Equal(t, "api_key", cap.header.Get("X-User-Role"))

	// A context with no role must still never produce a role-less internal
	// call (which the receiver would treat as admin).
	_, err = handlerByName(t, defs, "start_workflow")(tenantCtx(tenant, user), argsJSON(t, validArgs["start_workflow"]))
	require.NoError(t, err)
	assert.NotEmpty(t, cap.header.Get("X-User-Role"))
	assert.NotEqual(t, "admin", cap.header.Get("X-User-Role"))
}
