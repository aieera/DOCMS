// Phase 5 — pure-function tests for the retention SQL builder.
//
// What we pin without a database:
//   - Sweep query (no filters) excludes terminal lifecycle states +
//     legal-hold + retention_exempt docs.
//   - Each filter (class, tag, workspace, folder) adds its AND clause.
//   - Folder filter introduces the ltree-subtree JOIN.
//   - countOnly path returns "SELECT count(*)" rather than a
//     row-projection (drift between count and apply queries was the
//     classic legacy bug we're trying to design out).
//
// Integration tests (real Postgres + the actual sweep) live behind
// //go:build integration.
package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBuildRetentionMatchSQL_NoFilters(t *testing.T) {
	tenant := uuid.New()
	cutoff := time.Now().UTC().AddDate(0, 0, -365)
	sql, args := buildRetentionMatchSQL(buildRetentionMatchInput{
		tenantID:     tenant,
		cutoff:       &cutoff,
		limit:        500,
		selectIDOnly: true,
	})
	for _, want := range []string{
		"d.tenant_id = $1",
		"d.deleted_at IS NULL",
		"d.lifecycle_state IN ('active', 'retained')",
		"d.retention_exempt = false",
		"ORDER BY d.created_at ASC",
		"LIMIT 500",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected sweep SQL to contain %q\nGOT:\n%s", want, sql)
		}
	}
	// No filter args beyond tenant + cutoff.
	if len(args) != 2 {
		t.Errorf("no-filter sweep should have 2 args (tenant, cutoff); got %d", len(args))
	}
}

func TestBuildRetentionMatchSQL_AllFilters(t *testing.T) {
	tenant := uuid.New()
	ws := uuid.New()
	fld := uuid.New()
	cutoff := time.Now().UTC().AddDate(0, 0, -90)
	sql, args := buildRetentionMatchSQL(buildRetentionMatchInput{
		tenantID:     tenant,
		classFilter:  "invoice",
		tagFilter:    []string{"q1", "audit"},
		workspace:    &ws,
		folder:       &fld,
		cutoff:       &cutoff,
		limit:        25,
		selectIDOnly: true,
	})
	for _, want := range []string{
		"d.document_class = $",
		"d.tags && $",
		"d.workspace_id = $",
		// Folder filter uses a JOIN on the folders table with ltree
		// descendant operator <@. Both must appear together for the
		// folder-subtree semantic to actually take effect.
		"JOIN folders pf",
		"JOIN folders f",
		"f.path       OPERATOR(public.<@) pf.path",
		"d.created_at <= $",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected all-filters SQL to contain %q\nGOT:\n%s", want, sql)
		}
	}
	// Args order: tenant, class, tag-array, workspace, folder, cutoff.
	if len(args) != 6 {
		t.Errorf("all-filters sweep should have 6 args; got %d", len(args))
	}
}

func TestBuildRetentionMatchSQL_CountOnlyOmitsLimitAndOrder(t *testing.T) {
	tenant := uuid.New()
	cutoff := time.Now().UTC()
	sql, _ := buildRetentionMatchSQL(buildRetentionMatchInput{
		tenantID:  tenant,
		cutoff:    &cutoff,
		limit:     25, // intentionally set; countOnly must ignore it
		countOnly: true,
	})
	if !strings.HasPrefix(sql, "SELECT count(*) FROM documents d") {
		t.Errorf("count path must lead with SELECT count(*); got:\n%s", sql)
	}
	if strings.Contains(sql, "LIMIT") {
		t.Errorf("count path must not include LIMIT (would under-report); got:\n%s", sql)
	}
	if strings.Contains(sql, "ORDER BY") {
		t.Errorf("count path should skip ORDER BY (irrelevant for count); got:\n%s", sql)
	}
}

func TestBuildRetentionMatchSQL_FullProjectionForSample(t *testing.T) {
	tenant := uuid.New()
	cutoff := time.Now().UTC()
	sql, _ := buildRetentionMatchSQL(buildRetentionMatchInput{
		tenantID:   tenant,
		cutoff:     &cutoff,
		limit:      10,
		selectFull: true,
	})
	for _, want := range []string{
		"d.id",
		"d.title",
		"d.workspace_id",
		"d.folder_id",
		"d.lifecycle_state",
		"d.created_at",
		"LIMIT 10",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("expected sample-projection SQL to contain %q\nGOT:\n%s", want, sql)
		}
	}
}

// Class-only filter must NOT emit the folder JOIN — the JOIN is
// expensive and gating it on folder presence keeps the no-folder path
// the same shape as today's sweep.
func TestBuildRetentionMatchSQL_FolderJoinOnlyWhenFolderSet(t *testing.T) {
	tenant := uuid.New()
	cutoff := time.Now().UTC()
	sql, _ := buildRetentionMatchSQL(buildRetentionMatchInput{
		tenantID:    tenant,
		classFilter: "invoice",
		cutoff:      &cutoff,
		limit:       50,
		selectIDOnly: true,
	})
	if strings.Contains(sql, "JOIN folders") {
		t.Errorf("folder JOIN must only appear when folder filter is set; got:\n%s", sql)
	}
}
