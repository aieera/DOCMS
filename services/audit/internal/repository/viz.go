// Audit-trail visualization aggregation — ADR 0103 §18 F10.
//
// One query, one round-trip. Returns the JSON blob directly via
// `SELECT json_build_object(...)` so the handler just streams bytes.
// Phase 1 has no precomputed read-model; this runs each request.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// VizBucket is "hour" or "day". The handler validates before passing
// through; passing anything else here returns an error.
type VizBucket string

const (
	VizBucketHour VizBucket = "hour"
	VizBucketDay  VizBucket = "day"
)

// Viz aggregates audit_events for a single resource into the shape
// the frontend renders. Returns the JSON bytes verbatim so the
// handler doesn't have to model every nested struct in Go.
func (r *Repository) Viz(
	ctx context.Context,
	tenantID, resourceID string,
	bucket VizBucket,
	since time.Time,
) ([]byte, error) {
	switch bucket {
	case VizBucketHour, VizBucketDay:
	default:
		return nil, errors.New("bucket must be 'hour' or 'day'")
	}

	var out []byte
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
		WITH base AS (
		    SELECT ae.actor, ae.actor_name, ae.action, ae.created_at
		      FROM audit_events ae
		     WHERE ae.tenant_id    = $1
		       AND ae.resource_type = 'document'
		       AND ae.resource_id   = $2
		       AND ae.created_at   >= $3
		),
		by_time AS (
		    SELECT date_trunc($4, created_at) AS ts, COUNT(*) AS count
		      FROM base GROUP BY 1
		),
		by_actor AS (
		    SELECT actor, MIN(actor_name) AS name, COUNT(*) AS count
		      FROM base GROUP BY actor
		),
		by_action AS (
		    SELECT action, COUNT(*) AS count
		      FROM base GROUP BY action
		),
		sankey AS (
		    SELECT actor, action, COUNT(*) AS count
		      FROM base GROUP BY actor, action
		),
		heatmap AS (
		    SELECT EXTRACT(DOW  FROM created_at)::int AS dow,
		           EXTRACT(HOUR FROM created_at)::int AS hr,
		           COUNT(*) AS count
		      FROM base GROUP BY dow, hr
		)
		SELECT json_build_object(
		  'time_buckets',  COALESCE((SELECT json_agg(json_build_object('ts', ts, 'count', count) ORDER BY ts) FROM by_time), '[]'::json),
		  'actors',        COALESCE((SELECT json_agg(json_build_object('id', actor, 'name', name, 'count', count) ORDER BY count DESC) FROM by_actor), '[]'::json),
		  'actions',       COALESCE((SELECT json_agg(json_build_object('name', action, 'count', count) ORDER BY count DESC) FROM by_action), '[]'::json),
		  'sankey_edges',  COALESCE((SELECT json_agg(json_build_object('actor', actor, 'action', action, 'count', count) ORDER BY count DESC) FROM sankey), '[]'::json),
		  'heatmap',       COALESCE((SELECT json_agg(json_build_object('dow', dow, 'hour', hr, 'count', count)) FROM heatmap), '[]'::json),
		  'total_events',  (SELECT COUNT(*) FROM base)
		)::text`,
			tenantID, resourceID, since, string(bucket),
		).Scan(&out)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return out, nil
}
