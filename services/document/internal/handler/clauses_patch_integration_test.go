//go:build integration
// +build integration

// DEF-04 — PATCH /api/v1/clauses/{id} and approval. The product owner's
// rule: a change to a clause's WORDING clears its approval (an approver
// signed off on specific text, not on whatever the clause says later).
// The PATCH used to update body_text and bump version while leaving
// approved_by / approved_at in place, so edited text kept showing as
// approved. Metadata-only edits (name, jurisdiction, tags) and resending
// the same text keep the approval.
//
// Reuses the package fixture from clause_matches_integration_test.go
// (newTestTenant, doAuthedJSON, testPool, seedUserID).
//
// Run with: go test -tags integration -run TestClausePatch ./services/document/internal/handler/
package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
)

const approvedClauseText = "Each party shall keep the other party's Confidential Information secret."

// seedApprovedClause inserts a version-1 clause approved by the fixture
// user an hour ago.
func seedApprovedClause(ctx context.Context, t *testing.T, tenant uuid.UUID) (uuid.UUID, time.Time) {
	t.Helper()
	id := uuid.New()
	approvedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	err := database.WithTenantTx(ctx, testPool, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO clauses (tenant_id, id, name, body_text, jurisdiction, tags,
			                     created_by, approved_by, approved_at)
			VALUES ($1, $2, 'Confidentiality', $3, 'US', '{nda}', $4, $4, $5)`,
			tenant, id, approvedClauseText, seedUserID, approvedAt)
		return err
	})
	require.NoError(t, err)
	return id, approvedAt
}

type clauseState struct {
	body       string
	version    int
	approvedBy *uuid.UUID
	approvedAt *time.Time
}

func readClause(ctx context.Context, t *testing.T, tenant, id uuid.UUID) clauseState {
	t.Helper()
	var c clauseState
	require.NoError(t, testPool.QueryRow(ctx, `
		SELECT body_text, version, approved_by, approved_at
		  FROM clauses WHERE tenant_id = $1 AND id = $2`, tenant, id).
		Scan(&c.body, &c.version, &c.approvedBy, &c.approvedAt))
	return c
}

func patchClause(t *testing.T, id uuid.UUID, body string) {
	t.Helper()
	resp := doAuthedJSON(t, "PATCH", "/api/v1/clauses/"+id.String(), strings.NewReader(body), "member")
	require.Equal(t, 200, resp.Code, resp.Body.String())
}

func TestClausePatch_NewWording_ClearsApproval(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	id, _ := seedApprovedClause(ctx, t, tenant)

	resp := doAuthedJSON(t, "PATCH", "/api/v1/clauses/"+id.String(),
		strings.NewReader(`{"body_text":"Each party may disclose Confidential Information to affiliates.","tags":["nda","v2"]}`),
		"member")
	require.Equal(t, 200, resp.Code, resp.Body.String())
	body := decodeMap(t, resp)
	require.Nil(t, body["approved_by"], "response must show the clause as unapproved")
	require.Nil(t, body["approved_at"])

	c := readClause(ctx, t, tenant, id)
	require.Equal(t, "Each party may disclose Confidential Information to affiliates.", c.body)
	require.Equal(t, 2, c.version, "a wording change bumps the version")
	require.Nil(t, c.approvedBy, "new wording must clear approved_by")
	require.Nil(t, c.approvedAt, "new wording must clear approved_at")
}

func TestClausePatch_MetadataOnly_KeepsApproval(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	id, approvedAt := seedApprovedClause(ctx, t, tenant)

	patchClause(t, id, `{"tags":["nda","mutual"]}`)
	patchClause(t, id, `{"name":"Mutual confidentiality","jurisdiction":"UK"}`)

	c := readClause(ctx, t, tenant, id)
	require.Equal(t, approvedClauseText, c.body)
	require.Equal(t, 3, c.version, "metadata edits still bump the version")
	require.NotNil(t, c.approvedBy, "a metadata-only edit keeps the approval")
	require.Equal(t, seedUserID, *c.approvedBy)
	require.NotNil(t, c.approvedAt)
	require.True(t, approvedAt.Equal(*c.approvedAt), "approved_at must be untouched")
}

func TestClausePatch_SameWordingResent_KeepsApproval(t *testing.T) {
	ctx, tenant := newTestTenant(t)
	id, approvedAt := seedApprovedClause(ctx, t, tenant)

	patchClause(t, id, `{"body_text":`+jsonString(approvedClauseText)+`}`)

	c := readClause(ctx, t, tenant, id)
	require.Equal(t, approvedClauseText, c.body)
	require.NotNil(t, c.approvedBy, "resending the approved text is not a wording change")
	require.Equal(t, seedUserID, *c.approvedBy)
	require.NotNil(t, c.approvedAt)
	require.True(t, approvedAt.Equal(*c.approvedAt))
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
