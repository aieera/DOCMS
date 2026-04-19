package database

import (
	"net/url"
	"strings"
	"testing"
)

// TestWithMigrationsTable_SetsQueryParam pins the URL-rewriting logic
// that keeps each service's migrator on its own bookkeeping table.
// Breaking it re-introduces the shared-schema_migrations collision bug
// from STATE_OF_THE_PROJECT.
func TestWithMigrationsTable_SetsQueryParam(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "no existing query string",
			in:   "postgres://u:p@h:5432/db?sslmode=disable",
			want: "x-migrations-table=audit_schema_migrations",
		},
		{
			name: "preserves other params",
			in:   "postgres://u:p@h:5432/db?sslmode=disable&application_name=auth",
			want: "x-migrations-table=audit_schema_migrations",
		},
		{
			name: "overrides a pre-existing value",
			in:   "postgres://u:p@h:5432/db?x-migrations-table=wrong",
			want: "x-migrations-table=audit_schema_migrations",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := withMigrationsTable(tc.in, "audit_schema_migrations")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("missing %q in %q", tc.want, got)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("rewritten URL no longer parses: %v", err)
			}
			if u.Scheme != "postgres" {
				t.Errorf("scheme changed to %q", u.Scheme)
			}
			if u.Query().Get("x-migrations-table") != "audit_schema_migrations" {
				t.Errorf("query param not set: %s", u.Query())
			}
		})
	}
}

func TestServiceMigrationsTable_Convention(t *testing.T) {
	cases := map[string]string{
		"audit":     "audit_schema_migrations",
		"document":  "document_schema_migrations",
		"policy":    "policy_schema_migrations",
		"billing":   "billing_schema_migrations",
	}
	for svc, want := range cases {
		if got := ServiceMigrationsTable(svc); got != want {
			t.Errorf("ServiceMigrationsTable(%q) = %q, want %q", svc, got, want)
		}
	}
}
