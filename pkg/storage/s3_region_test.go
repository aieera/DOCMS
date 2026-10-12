package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// credentialScope returns the region segment of a presigned URL's
// X-Amz-Credential (<key>/<date>/<region>/s3/aws4_request) — the region
// the client signs with. Presigning with an explicit region is local
// (no GetBucketLocation round-trip), so this needs no S3 endpoint.
func credentialScope(t *testing.T, s *S3Client, which string) string {
	t.Helper()
	var (
		u   string
		err error
	)
	if which == "put" {
		u, err = s.GeneratePresignedPutURL(context.Background(), "bucket", "key", time.Minute)
	} else {
		u, err = s.GeneratePresignedGetURL(context.Background(), "bucket", "key", time.Minute)
	}
	require.NoError(t, err)
	i := strings.Index(u, "X-Amz-Credential=")
	require.GreaterOrEqual(t, i, 0, "presigned URL has no credential: %s", u)
	cred := u[i+len("X-Amz-Credential="):]
	if j := strings.IndexByte(cred, '&'); j >= 0 {
		cred = cred[:j]
	}
	parts := strings.Split(strings.ReplaceAll(cred, "%2F", "/"), "/")
	require.Len(t, parts, 5, "credential scope: %s", cred)
	return parts[2]
}

// TestNewS3_UsesConfiguredRegion pins CB-9: both clients (server-side and
// presign) sign with the configured region; us-east-1 is only the
// fallback when nothing is configured. A hard-coded us-east-1 makes every
// request against a real AWS bucket in another region fail.
func TestNewS3_UsesConfiguredRegion(t *testing.T) {
	t.Run("explicit region on both clients", func(t *testing.T) {
		s, err := NewS3ClientWithRegion("minio:9000", "localhost:9000", "AKIA", "secret", false, "me-central-1")
		require.NoError(t, err)
		require.Equal(t, "me-central-1", credentialScope(t, s, "put"))
		require.Equal(t, "me-central-1", credentialScope(t, s, "get"))
	})
	t.Run("env region for legacy constructors", func(t *testing.T) {
		t.Setenv("SEDOC_S3_REGION", "eu-west-2")
		t.Setenv("AWS_REGION", "ap-south-1")
		s, err := NewS3Client("minio:9000", "AKIA", "secret", false)
		require.NoError(t, err)
		require.Equal(t, "eu-west-2", credentialScope(t, s, "put"))
	})
	t.Run("AWS_REGION when SEDOC_S3_REGION unset", func(t *testing.T) {
		t.Setenv("SEDOC_S3_REGION", "")
		t.Setenv("AWS_REGION", "me-central-1")
		s, err := NewS3ClientWithPublicEndpoint("minio:9000", "localhost:9000", "AKIA", "secret", false)
		require.NoError(t, err)
		require.Equal(t, "me-central-1", credentialScope(t, s, "put"))
	})
	t.Run("us-east-1 only when empty", func(t *testing.T) {
		t.Setenv("SEDOC_S3_REGION", "")
		t.Setenv("AWS_REGION", "")
		s, err := NewS3ClientWithRegion("minio:9000", "", "AKIA", "secret", false, "")
		require.NoError(t, err)
		require.Equal(t, "us-east-1", credentialScope(t, s, "put"))
	})
}
