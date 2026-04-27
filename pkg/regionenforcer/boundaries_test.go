package regionenforcer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBoundaryOf_KnownRegions(t *testing.T) {
	cases := map[string]Boundary{
		"us-east-1":      BoundaryUS,
		"us-west-2":      BoundaryUS,
		"eu-west-1":      BoundaryEU,
		"eu-central-1":   BoundaryEU,
		"me-south-1":     BoundaryMENA,
		"ap-southeast-1": BoundaryAPAC,
		"ap-northeast-1": BoundaryAPAC,
		"custom":         BoundaryOther,
	}
	for region, want := range cases {
		require.Equal(t, want, BoundaryOf(region), "region=%s", region)
	}
}

func TestBoundaryOf_UnknownFallsToOther(t *testing.T) {
	require.Equal(t, BoundaryOther, BoundaryOf("mars-south-1"))
	require.Equal(t, BoundaryOther, BoundaryOf(""))
}

func TestSameBoundary(t *testing.T) {
	require.True(t, SameBoundary("us-east-1", "us-west-2"))
	require.True(t, SameBoundary("eu-west-1", "eu-central-1"))
	require.False(t, SameBoundary("us-east-1", "eu-west-1"))
	require.False(t, SameBoundary("me-south-1", "us-east-1"))
	// OTHER never matches OTHER: custom regions must specify their
	// own policy rather than inheriting one.
	require.False(t, SameBoundary("custom", "custom"))
}

func TestValidate_SameRegion_OK(t *testing.T) {
	require.NoError(t, Validate("eu-west-1", "eu-west-1", "storage"))
}

func TestValidate_UnknownRegion_Rejects(t *testing.T) {
	err := Validate("eu-west-1", "bogus", "storage")
	require.Error(t, err)
	var v *ErrRegionViolation
	require.ErrorAs(t, err, &v)
	require.Equal(t, "unknown_region", v.Reason)
}

func TestValidate_CrossBoundary_Rejects(t *testing.T) {
	err := Validate("eu-west-1", "us-east-1", "storage")
	require.Error(t, err)
	var v *ErrRegionViolation
	require.ErrorAs(t, err, &v)
	require.Equal(t, "cross_boundary", v.Reason)
}

// TestBoundariesMatchMigration pins the Go mirror to the SQL seed.
// If a region is added to supported_regions without updating the Go
// map (or vice versa), this test fails and blocks merge — exactly the
// guard the package comment promises.
func TestBoundariesMatchMigration(t *testing.T) {
	path := filepath.Join("..", "..", "services", "document", "migrations",
		"000014_region_governance.up.sql")
	body, err := os.ReadFile(path)
	require.NoError(t, err, "cannot read migration")

	// Match INSERT rows: ('code', 'display', 'BOUNDARY').
	re := regexp.MustCompile(`\('([a-z0-9\-]+)',\s*'[^']+',\s*'([A-Z]+)'\)`)
	matches := re.FindAllStringSubmatch(string(body), -1)
	require.NotEmpty(t, matches, "no INSERT rows parsed from migration")

	sqlSide := map[string]Boundary{}
	for _, m := range matches {
		sqlSide[strings.ToLower(m[1])] = Boundary(m[2])
	}
	require.Equal(t, sqlSide, regionBoundary,
		"Go mirror drifted from supported_regions seed — update both")
}
