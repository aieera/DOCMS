// Package analytics is the governed query layer (ADR 0119): a
// whitelist of DATASETS, each declaring the dimensions, measures, and
// filterable fields it exposes, compiled server-side into parameterized
// SQL. Clients never send SQL — only registry names — so the API is
// safe by construction: identifiers come exclusively from this file,
// values travel as bind parameters, and every query carries the tenant
// predicate AND runs inside WithTenantTx (RLS is the second lock).
package analytics

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dimension is a groupable column (or expression) of a dataset.
type Dimension struct {
	Name string // API name, e.g. "document_class"
	SQL  string // SELECT/GROUP BY expression, e.g. "document_class"
}

// Measure is an aggregate of a dataset.
type Measure struct {
	Name string // e.g. "count", "total_size_bytes"
	SQL  string // e.g. "count(*)", "coalesce(sum(size_bytes),0)"
}

// Dataset is one queryable source.
type Dataset struct {
	Name string
	// From is the FROM clause body (table + optional joins). The
	// driving table must be aliased `t` and expose t.tenant_id and
	// t.created_at (the time-range column).
	From string
	// BaseWhere holds dataset-invariant predicates (soft-delete etc.),
	// AND-ed after the tenant predicate. No user input ever lands here.
	BaseWhere  string
	Dimensions map[string]Dimension
	Measures   map[string]Measure
}

// Registry is the full whitelist, keyed by dataset name.
var Registry = map[string]Dataset{
	"documents": {
		Name:      "documents",
		From:      "documents t",
		BaseWhere: "t.deleted_at IS NULL",
		Dimensions: dims(
			Dimension{Name: "document_class", SQL: "t.document_class"},
			Dimension{Name: "lifecycle_state", SQL: "t.lifecycle_state"},
			Dimension{Name: "doc_type", SQL: "t.doc_type"},
			Dimension{Name: "mime_type", SQL: "coalesce(t.mime_type,'')"},
			Dimension{Name: "region_pin", SQL: "t.region_pin"},
			Dimension{Name: "workspace_id", SQL: "t.workspace_id::text"},
			Dimension{Name: "created_by", SQL: "coalesce(t.created_by::text,'')"},
			Dimension{Name: "created_day", SQL: "to_char(date_trunc('day', t.created_at), 'YYYY-MM-DD')"},
			Dimension{Name: "created_month", SQL: "to_char(date_trunc('month', t.created_at), 'YYYY-MM')"},
		),
		Measures: meas(
			Measure{Name: "count", SQL: "count(*)"},
			Measure{Name: "total_size_bytes", SQL: "coalesce(sum(t.total_size_bytes),0)"},
		),
	},
	"versions": {
		Name: "versions",
		// Join documents for workspace scoping dims; the version row
		// carries its own tenant_id for the predicate.
		From:      "versions t JOIN documents d ON d.tenant_id = t.tenant_id AND d.id = t.document_id",
		BaseWhere: "d.deleted_at IS NULL",
		Dimensions: dims(
			Dimension{Name: "mime_type", SQL: "coalesce(t.mime_type,'')"},
			Dimension{Name: "workspace_id", SQL: "d.workspace_id::text"},
			Dimension{Name: "document_class", SQL: "d.document_class"},
			Dimension{Name: "created_by", SQL: "coalesce(t.created_by::text,'')"},
			Dimension{Name: "created_day", SQL: "to_char(date_trunc('day', t.created_at), 'YYYY-MM-DD')"},
			Dimension{Name: "created_month", SQL: "to_char(date_trunc('month', t.created_at), 'YYYY-MM')"},
		),
		Measures: meas(
			Measure{Name: "count", SQL: "count(*)"},
			Measure{Name: "total_size_bytes", SQL: "coalesce(sum(t.size_bytes),0)"},
			Measure{Name: "avg_size_bytes", SQL: "coalesce(avg(t.size_bytes),0)::bigint"},
		),
	},
	"tasks": {
		Name:      "tasks",
		From:      "tasks t",
		BaseWhere: "",
		Dimensions: dims(
			Dimension{Name: "status", SQL: "t.status"},
			Dimension{Name: "priority", SQL: "t.priority"},
			Dimension{Name: "assignee_id", SQL: "coalesce(t.assignee_id::text,'')"},
			Dimension{Name: "created_day", SQL: "to_char(date_trunc('day', t.created_at), 'YYYY-MM-DD')"},
			Dimension{Name: "created_month", SQL: "to_char(date_trunc('month', t.created_at), 'YYYY-MM')"},
		),
		Measures: meas(
			Measure{Name: "count", SQL: "count(*)"},
		),
	},
}

func dims(ds ...Dimension) map[string]Dimension {
	m := make(map[string]Dimension, len(ds))
	for _, d := range ds {
		m[d.Name] = d
	}
	return m
}

func meas(ms ...Measure) map[string]Measure {
	m := make(map[string]Measure, len(ms))
	for _, x := range ms {
		m[x.Name] = x
	}
	return m
}

// ---- query spec + compiler -------------------------------------------

const (
	DefaultLimit = 100
	MaxLimit     = 1000
	// MaxFilterValues bounds one filter's IN-list.
	MaxFilterValues = 50
)

// Query is the client-supplied spec (all names resolved via Registry).
type Query struct {
	Dataset    string              `json:"dataset"`
	Dimensions []string            `json:"dimensions"`
	Measures   []string            `json:"measures"`
	Filters    map[string][]string `json:"filters,omitempty"` // dimension name → allowed values (IN)
	TimeRange  *TimeRange          `json:"time_range,omitempty"`
	Limit      int                 `json:"limit,omitempty"`
}

type TimeRange struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

// Compiled is the executable output: SQL + args + result column names.
type Compiled struct {
	SQL     string
	Args    []any
	Columns []string
}

// Compile validates the spec against the registry and produces
// parameterized SQL. tenantID is always $1.
func Compile(q Query, tenantID any) (*Compiled, error) {
	ds, ok := Registry[q.Dataset]
	if !ok {
		return nil, fmt.Errorf("unknown dataset %q", q.Dataset)
	}
	if len(q.Measures) == 0 {
		return nil, fmt.Errorf("at least one measure is required")
	}
	if len(q.Dimensions) > 3 {
		return nil, fmt.Errorf("at most 3 dimensions")
	}

	var selects, groupBys, cols []string
	for _, name := range q.Dimensions {
		d, ok := ds.Dimensions[name]
		if !ok {
			return nil, fmt.Errorf("dataset %q has no dimension %q", q.Dataset, name)
		}
		selects = append(selects, d.SQL+" AS "+d.Name)
		groupBys = append(groupBys, d.SQL)
		cols = append(cols, d.Name)
	}
	for _, name := range q.Measures {
		m, ok := ds.Measures[name]
		if !ok {
			return nil, fmt.Errorf("dataset %q has no measure %q", q.Dataset, name)
		}
		selects = append(selects, m.SQL+" AS "+m.Name)
		cols = append(cols, m.Name)
	}

	args := []any{tenantID}
	where := []string{"t.tenant_id = $1"}
	if ds.BaseWhere != "" {
		where = append(where, ds.BaseWhere)
	}

	// Filters — deterministic order (sorted by dimension name) so the
	// compiled SQL is stable/goldenable.
	fnames := make([]string, 0, len(q.Filters))
	for name := range q.Filters {
		fnames = append(fnames, name)
	}
	sort.Strings(fnames)
	for _, name := range fnames {
		d, ok := ds.Dimensions[name]
		if !ok {
			return nil, fmt.Errorf("dataset %q has no filterable dimension %q", q.Dataset, name)
		}
		vals := q.Filters[name]
		if len(vals) == 0 {
			continue
		}
		if len(vals) > MaxFilterValues {
			return nil, fmt.Errorf("filter %q: more than %d values", name, MaxFilterValues)
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			args = append(args, v)
			ph[i] = fmt.Sprintf("$%d", len(args))
		}
		where = append(where, fmt.Sprintf("%s IN (%s)", d.SQL, strings.Join(ph, ", ")))
	}

	if q.TimeRange != nil {
		if q.TimeRange.From != nil {
			args = append(args, *q.TimeRange.From)
			where = append(where, fmt.Sprintf("t.created_at >= $%d", len(args)))
		}
		if q.TimeRange.To != nil {
			args = append(args, *q.TimeRange.To)
			where = append(where, fmt.Sprintf("t.created_at < $%d", len(args)))
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(selects, ", "))
	b.WriteString(" FROM ")
	b.WriteString(ds.From)
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(where, " AND "))
	if len(groupBys) > 0 {
		b.WriteString(" GROUP BY ")
		b.WriteString(strings.Join(groupBys, ", "))
	}
	// Order by the first measure, descending — a stable, useful default
	// for "top N" reads.
	firstMeasure := ds.Measures[q.Measures[0]]
	b.WriteString(" ORDER BY " + firstMeasure.SQL + " DESC")
	if len(groupBys) > 0 {
		// Deterministic tiebreak so pagination-free LIMIT is stable.
		b.WriteString(", " + strings.Join(groupBys, ", "))
	}
	fmt.Fprintf(&b, " LIMIT %d", limit)

	return &Compiled{SQL: b.String(), Args: args, Columns: cols}, nil
}

// DatasetsDTO describes the registry for the report-builder UI.
func DatasetsDTO() []map[string]any {
	names := make([]string, 0, len(Registry))
	for n := range Registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		ds := Registry[n]
		var dnames, mnames []string
		for d := range ds.Dimensions {
			dnames = append(dnames, d)
		}
		for m := range ds.Measures {
			mnames = append(mnames, m)
		}
		sort.Strings(dnames)
		sort.Strings(mnames)
		out = append(out, map[string]any{
			"name": n, "dimensions": dnames, "measures": mnames,
		})
	}
	return out
}
