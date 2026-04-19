package middleware

import (
	"context"
	"fmt"
	"strings"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// RegionEnforcer validates that a resource's region pin is compatible with
// the storage bucket / search index / cache keyspace actually being
// addressed. The naming convention is "<product>-<region>-<tier>", e.g.
// "dms-us-east-1-hot". If any component's region disagrees with pin, the
// call is rejected with ErrRegionViolation.
type RegionEnforcer struct {
	defaultRegion string
}

// NewRegionEnforcer constructs an enforcer scoped to a default region. The
// default is used when an operation doesn't specify a region pin explicitly.
func NewRegionEnforcer(defaultRegion string) *RegionEnforcer {
	return &RegionEnforcer{defaultRegion: defaultRegion}
}

// CheckBucket validates that bucketName belongs to the pinned region.
func (r *RegionEnforcer) CheckBucket(_ context.Context, bucketName, pin string) error {
	if pin == "" {
		pin = r.defaultRegion
	}
	region := regionFromBucket(bucketName)
	if region == "" {
		return fmt.Errorf("cannot determine region from bucket %q: %w", bucketName, vdmserr.ErrRegionViolation)
	}
	if region != pin {
		return fmt.Errorf("bucket %s is in %s but tenant pin is %s: %w",
			bucketName, region, pin, vdmserr.ErrRegionViolation)
	}
	return nil
}

// CheckIndex validates that an OpenSearch / Qdrant collection name is in pin.
// Index naming follows "vaultdms-<region>-<logical>".
func (r *RegionEnforcer) CheckIndex(_ context.Context, indexName, pin string) error {
	if pin == "" {
		pin = r.defaultRegion
	}
	region := regionFromIndex(indexName)
	if region == "" {
		return fmt.Errorf("cannot determine region from index %q: %w", indexName, vdmserr.ErrRegionViolation)
	}
	if region != pin {
		return fmt.Errorf("index %s is in %s but tenant pin is %s: %w",
			indexName, region, pin, vdmserr.ErrRegionViolation)
	}
	return nil
}

// regionFromBucket extracts "us-east-1" from "dms-us-east-1-hot".
func regionFromBucket(name string) string {
	// drop product prefix
	name = strings.TrimPrefix(name, "dms-")
	// drop tier suffix (last segment)
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return ""
	}
	return name[:i]
}

// regionFromIndex extracts "us-east-1" from "vaultdms-us-east-1-documents".
func regionFromIndex(name string) string {
	parts := strings.SplitN(strings.TrimPrefix(name, "vaultdms-"), "-", 4)
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[:3], "-")
}
