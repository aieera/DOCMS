//go:build integration
// +build integration

// Acceptance tests for Workstream 6 (folder hierarchy at 100k). Pins:
//
//  1. listing children of a parent with 100k direct children is paginated
//     (bounded page) and fast — never materialises all 100k.
//  2. keyset pagination is correct: page 2 resumes after page 1 with no overlap;
//     page size is clamped (default 50, max 200).
//  3. child + document counts are O(1) per listing (two GROUP BY aggregates),
//     not an N+1 per row — and they're accurate.
//
// Run with: go test -tags integration ./services/document/internal/service/...
//
// Reuses externalKeyFixture + callerUser from external_key_upsert_test.go.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestListFolders_KeysetPaginationAt100k(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, rootID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	const N = 100000
	_, err := pool.Exec(ctx, `
		INSERT INTO folders (id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
		                     created_by, created_at, updated_by, updated_at, visibility)
		SELECT gen_random_uuid(), $1, $2, $3, ('root.c'||g)::ltree,
		       'child_'||to_char(g,'FM000000'), 1, $4, now(), $4, now(), 'shared'
		  FROM generate_series(1, $5) g
	`, tenant, wsID, rootID, user, N)
	require.NoError(t, err)

	// Page 1: default page size (50), ordered by name, and bounded/fast even
	// though the parent has 100k children.
	start := time.Now()
	p1, tok1, err := svc.ListFolders(callCtx, wsID, &rootID, "", 0)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Len(t, p1, 50, "default page size, not all 100k")
	require.NotEmpty(t, tok1, "a full page returns a next cursor")
	require.Less(t, elapsed, 5*time.Second, "a bounded page must be fast at 100k")
	require.Equal(t, "child_000001", p1[0].Name)
	require.Equal(t, "child_000050", p1[49].Name)
	for i := 1; i < len(p1); i++ {
		require.Less(t, p1[i-1].Name, p1[i].Name, "page ordered by name")
	}

	// Page 2 resumes immediately after page 1 — no overlap, no gap.
	p2, _, err := svc.ListFolders(callCtx, wsID, &rootID, tok1, 0)
	require.NoError(t, err)
	require.Len(t, p2, 50)
	require.Equal(t, "child_000051", p2[0].Name)

	// Limit clamping: 0 → default 50, 9999 → max 200.
	pMax, _, err := svc.ListFolders(callCtx, wsID, &rootID, "", 9999)
	require.NoError(t, err)
	require.Len(t, pMax, 200, "limit capped at 200")
}

func TestListFolders_CountsAreAggregatedNotN1(t *testing.T) {
	callCtx, svc, pool, tenant, wsID, rootID := externalKeyFixture(t)
	ctx := context.Background()
	user := callerUser(t, callCtx)

	// One child with a known id, named to sort first.
	child := uuid.Must(uuid.NewV7())
	_, err := pool.Exec(ctx, `
		INSERT INTO folders (id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
		                     created_by, created_at, updated_by, updated_at, visibility)
		VALUES ($1, $2, $3, $4, 'root.aaa'::ltree, 'aaa_child', 1, $5, now(), $5, now(), 'shared')
	`, child, tenant, wsID, rootID, user)
	require.NoError(t, err)

	// 3 grandchildren + 2 documents directly under that child.
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
		                     created_by, created_at, updated_by, updated_at, visibility)
		SELECT gen_random_uuid(), $1, $2, $3, ('root.aaa.g'||g)::ltree, 'gc_'||g, 2, $4, now(), $4, now(), 'shared'
		  FROM generate_series(1, 3) g
	`, tenant, wsID, child, user)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO documents (tenant_id, workspace_id, folder_id, title)
		SELECT $1, $2, $3, 'doc'||g FROM generate_series(1, 2) g
	`, tenant, wsID, child)
	require.NoError(t, err)

	// A couple of sibling children with no descendants → counts must be 0.
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
		                     created_by, created_at, updated_by, updated_at, visibility)
		SELECT gen_random_uuid(), $1, $2, $3, ('root.z'||g)::ltree, 'zzz_'||g, 1, $4, now(), $4, now(), 'shared'
		  FROM generate_series(1, 2) g
	`, tenant, wsID, rootID, user)
	require.NoError(t, err)

	page, _, err := svc.ListFolders(callCtx, wsID, &rootID, "", 50)
	require.NoError(t, err)
	require.NotEmpty(t, page)
	require.Equal(t, "aaa_child", page[0].Name)
	require.Equal(t, int64(3), page[0].ChildFolderCount, "child count from aggregate")
	require.Equal(t, int64(2), page[0].DocumentCount, "doc count from aggregate")
	// Childless siblings report zero (the aggregate simply omits them).
	for _, f := range page {
		if f.Name == "zzz_1" || f.Name == "zzz_2" {
			require.Equal(t, int64(0), f.ChildFolderCount)
			require.Equal(t, int64(0), f.DocumentCount)
		}
	}
}
