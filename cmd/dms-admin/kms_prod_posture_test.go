//go:build integration && prodposture
// +build integration,prodposture

// O-7 (2026-10-12 review addendum) — tenant_keks is FORCE ROW LEVEL
// SECURITY since document migration 000108, so even the table owner sees
// only the rows of the tenant in app.current_tenant. The kms commands
// must therefore run in the target tenant's context, whatever role the
// operator connects as (the dms_app runtime role here).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

func TestProdPosture_KMSCommandsRunInTenantContext(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	if err := database.RunMigrations(db.SuperDSN, "../../services/document/migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;`); err != nil {
		t.Fatalf("grant: %v", err)
	}
	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	for _, id := range []uuid.UUID{tenantA, tenantB} {
		if _, err := db.Super.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 't', $2)`,
			id, "t-"+id.String()); err != nil {
			t.Fatalf("seed org: %v", err)
		}
	}

	for _, id := range []uuid.UUID{tenantA, tenantB} {
		alias, err := createKEK(ctx, db.App, id)
		if err != nil {
			t.Fatalf("kms create as dms_app: %v", err)
		}
		if want := "vaultdms/tenant/" + id.String(); alias != want {
			t.Fatalf("alias = %q, want %q", alias, want)
		}
	}

	retired, live, alias, err := rotateKEK(ctx, db.App, tenantA)
	if err != nil {
		t.Fatalf("kms rotate as dms_app: %v", err)
	}
	if retired != 1 || live != 2 || !strings.HasSuffix(alias, "@v2") {
		t.Fatalf("rotate = (v%d → v%d, %q), want v1 → v2 @v2", retired, live, alias)
	}

	one, err := listKEKs(ctx, db.App, tenantA)
	if err != nil {
		t.Fatalf("kms list --tenant: %v", err)
	}
	if len(one) != 2 || one[0].Retired == nil || one[1].Retired != nil {
		t.Fatalf("tenant A history = %+v, want v1 retired + v2 live", one)
	}

	all, err := listKEKs(ctx, db.App, uuid.Nil)
	if err != nil {
		t.Fatalf("kms list: %v", err)
	}
	perTenant := map[uuid.UUID]int{}
	for _, k := range all {
		perTenant[k.TenantID]++
	}
	if perTenant[tenantA] != 2 || perTenant[tenantB] != 1 {
		t.Fatalf("kms list (all tenants) = %v, want A:2 B:1", perTenant)
	}

	target, pending, err := rewrapPlan(ctx, db.App, tenantA, 10)
	if err != nil {
		t.Fatalf("kms rewrap plan: %v", err)
	}
	if target != alias || len(pending) != 0 {
		t.Fatalf("rewrap plan = (%q, %d blobs), want (%q, 0)", target, len(pending), alias)
	}
}
