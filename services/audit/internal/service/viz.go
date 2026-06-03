// Audit-trail visualization — service-layer passthrough (ADR 0103).
package service

import (
	"context"
	"time"

	"github.com/aieera/sedoc/services/audit/internal/repository"
)

// Viz returns the aggregated JSON blob for a document. `since` is
// the cutoff timestamp; pass a zero time to get everything.
func (s *Service) Viz(
	ctx context.Context,
	tenantID, resourceID string,
	bucket string,
	since time.Time,
) ([]byte, error) {
	return s.repo.Viz(ctx, tenantID, resourceID, repository.VizBucket(bucket), since)
}
