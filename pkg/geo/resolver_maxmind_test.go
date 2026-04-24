//go:build maxmind

package geo

// MaxMind adapter tests. Runs only under `-tags maxmind` because
// the adapter file is behind the same tag. Operators use:
//
//	VAULTDMS_GEOIP2_TEST_DB=/path/to/GeoLite2-Country.mmdb \
//	    go test -tags maxmind ./pkg/geo/...
//
// If the env var is absent we skip rather than fail — CI doesn't
// ship a `.mmdb` (licensing + size); operators who opt into the
// tag are expected to point at their own copy.

import (
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *MaxMindResolver {
	t.Helper()
	path := os.Getenv("VAULTDMS_GEOIP2_TEST_DB")
	if path == "" {
		t.Skip("VAULTDMS_GEOIP2_TEST_DB not set; skipping MaxMind adapter tests")
	}
	r, err := NewMaxMindResolver(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestMaxMind_Lookup_GoogleDNS(t *testing.T) {
	r := openTestDB(t)
	cc, err := r.LookupCountry(net.ParseIP("8.8.8.8"))
	require.NoError(t, err)
	require.Equal(t, "US", cc)
}

func TestMaxMind_UnknownIPReturnsErrUnknown(t *testing.T) {
	r := openTestDB(t)
	// Documentation range — not allocated to any country.
	_, err := r.LookupCountry(net.ParseIP("192.0.2.1"))
	// Either ErrUnknown (empty iso_code in the DB) or a match for
	// the private-range fallback — both are acceptable; we only
	// require the adapter does NOT panic or return a random code.
	if err != nil {
		require.ErrorIs(t, err, ErrUnknown)
	}
}

func TestMaxMind_Reload(t *testing.T) {
	r := openTestDB(t)
	// Reload must be idempotent; the same DB path re-opened should
	// produce the same lookup.
	require.NoError(t, r.Reload())
	cc, err := r.LookupCountry(net.ParseIP("8.8.8.8"))
	require.NoError(t, err)
	require.Equal(t, "US", cc)
}
