// Package bucket shards customers under intermediate folders so no single parent
// in SeDoc holds tens of thousands of direct children (mirrors SeDoc's own WS6
// folder-bucketing). Deterministic: same customer_ref → same bucket label.
package bucket

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// DefaultBuckets is the shard count when unset.
const DefaultBuckets = 256

// Label returns the bucket folder label for a customer. FNV-32a hash mod N keeps
// the distribution uniform even for sequential/skewed customer ids. With N=256
// and 100k customers that's ~400 customers per bucket.
func Label(customerRef string, buckets int) string {
	if buckets <= 0 {
		buckets = DefaultBuckets
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(customerRef))))
	return fmt.Sprintf("b%03d", h.Sum32()%uint32(buckets))
}
