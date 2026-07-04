package analytics

// Golden compiler tests — the DoD's "matches a hand-checked query"
// hook: the exact SQL + bind args for representative specs are pinned
// here and can be eyeballed / run by hand against a tenant DB.

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const tenant = "11111111-1111-1111-1111-111111111111"

func TestCompile_GoldenDocumentsByClass(t *testing.T) {
	c, err := Compile(Query{
		Dataset:    "documents",
		Dimensions: []string{"document_class"},
		Measures:   []string{"count"},
	}, tenant)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT t.document_class AS document_class, count(*) AS count " +
		"FROM documents t " +
		"WHERE t.tenant_id = $1 AND t.deleted_at IS NULL " +
		"GROUP BY t.document_class " +
		"ORDER BY count(*) DESC, t.document_class LIMIT 100"
	if c.SQL != want {
		t.Fatalf("SQL mismatch:\n got: %s\nwant: %s", c.SQL, want)
	}
	if !reflect.DeepEqual(c.Args, []any{tenant}) {
		t.Fatalf("args: %v", c.Args)
	}
	if !reflect.DeepEqual(c.Columns, []string{"document_class", "count"}) {
		t.Fatalf("columns: %v", c.Columns)
	}
}

func TestCompile_GoldenVersionsFiltered(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	c, err := Compile(Query{
		Dataset:    "versions",
		Dimensions: []string{"created_month", "mime_type"},
		Measures:   []string{"count", "total_size_bytes"},
		Filters: map[string][]string{
			"mime_type":      {"application/pdf", "image/png"},
			"document_class": {"invoice"},
		},
		TimeRange: &TimeRange{From: &from, To: &to},
		Limit:     10,
	}, tenant)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT to_char(date_trunc('month', t.created_at), 'YYYY-MM') AS created_month, " +
		"coalesce(t.mime_type,'') AS mime_type, " +
		"count(*) AS count, coalesce(sum(t.size_bytes),0) AS total_size_bytes " +
		"FROM versions t JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id " +
		"WHERE t.tenant_id = $1 AND d.deleted_at IS NULL " +
		"AND d.document_class IN ($2) " +
		"AND coalesce(t.mime_type,'') IN ($3, $4) " +
		"AND t.created_at >= $5 AND t.created_at < $6 " +
		"GROUP BY to_char(date_trunc('month', t.created_at), 'YYYY-MM'), coalesce(t.mime_type,'') " +
		"ORDER BY count(*) DESC, to_char(date_trunc('month', t.created_at), 'YYYY-MM'), coalesce(t.mime_type,'') " +
		"LIMIT 10"
	if c.SQL != want {
		t.Fatalf("SQL mismatch:\n got: %s\nwant: %s", c.SQL, want)
	}
	if !reflect.DeepEqual(c.Args, []any{tenant, "invoice", "application/pdf", "image/png", from, to}) {
		t.Fatalf("args: %v", c.Args)
	}
}

func TestCompile_TenantPredicateAlwaysFirst(t *testing.T) {
	for name := range Registry {
		c, err := Compile(Query{Dataset: name, Measures: []string{"count"}}, tenant)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(c.SQL, "t.tenant_id = $1") {
			t.Fatalf("%s: missing tenant predicate: %s", name, c.SQL)
		}
		if c.Args[0] != tenant {
			t.Fatalf("%s: tenant not $1", name)
		}
	}
}

func TestCompile_Rejections(t *testing.T) {
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"unknown dataset", Query{Dataset: "users", Measures: []string{"count"}}, "unknown dataset"},
		{"no measures", Query{Dataset: "documents"}, "at least one measure"},
		{"unknown dimension", Query{Dataset: "documents", Dimensions: []string{"password"}, Measures: []string{"count"}}, "no dimension"},
		{"unknown measure", Query{Dataset: "documents", Measures: []string{"drop table"}}, "no measure"},
		{"unknown filter field", Query{Dataset: "documents", Measures: []string{"count"}, Filters: map[string][]string{"sql": {"x"}}}, "no filterable dimension"},
		{"too many dimensions", Query{Dataset: "documents", Dimensions: []string{"document_class", "lifecycle_state", "doc_type", "region_pin"}, Measures: []string{"count"}}, "at most 3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compile(c.q, tenant)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestCompile_LimitClamped(t *testing.T) {
	c, err := Compile(Query{Dataset: "tasks", Measures: []string{"count"}, Limit: 99999}, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(c.SQL, "LIMIT 1000") {
		t.Fatalf("limit not clamped: %s", c.SQL)
	}
}

func TestCompile_FilterValueCountCapped(t *testing.T) {
	vals := make([]string, MaxFilterValues+1)
	for i := range vals {
		vals[i] = "x"
	}
	_, err := Compile(Query{
		Dataset: "documents", Measures: []string{"count"},
		Filters: map[string][]string{"document_class": vals},
	}, tenant)
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("want filter cap error, got %v", err)
	}
}
