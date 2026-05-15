// Package storage wraps the minio-go S3 client with the surface area VaultDMS
// services need: put/get/delete, presigned URLs, bucket management, and
// object metadata. It is used for both MinIO (on-prem) and real S3.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectInfo is the subset of object metadata the platform uses.
type ObjectInfo struct {
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
}

// S3Client is the minio-go wrapper.
//
// Two clients live inside, sharing credentials:
//
//   - c: the "internal" client. Used for every server-side operation —
//     PutObject, GetObject, BucketExists, EnsureBucket, etc. Endpoint
//     is the address the storage service can reach from inside its
//     deploy environment (e.g. "minio:9000" in docker-compose).
//
//   - presign: the client used ONLY for GeneratePresignedPutURL /
//     GeneratePresignedGetURL. Endpoint is whatever address the
//     end-user's BROWSER can reach (in dev: "localhost:9000"). When
//     PublicEndpoint is empty this points at the same client as c.
//
// Why two clients (instead of host-rewrite-after-sign): AWS Sig V4
// binds the canonical request to the Host header. MinIO validates
// strictly — a string-replaced URL host produces a 403
// SignatureDoesNotMatch on the browser PUT. The only working path is
// to sign with the host the browser will actually use. We tested
// the rewrite path; MinIO rejected it.
//
// Region is set explicitly on BOTH clients so PresignedPutObject does
// NOT make a GetBucketLocation HTTP call before signing. The presign
// client uses the public endpoint hostname (e.g. "localhost:9000")
// which is NOT reachable from inside the storage container —
// GetBucketLocation would dial-out and fail "connection refused"
// (the failure mode of the first two-client attempt).
type S3Client struct {
	c       *minio.Client
	presign *minio.Client
}

// NewS3Client constructs a MinIO / S3 client.
//
// Credential resolution:
//   - If accessKey AND secretKey are both non-empty → static creds.
//     Use this for MinIO and for any deployment that injects long-lived
//     keys via env / secret store.
//   - If both are empty → AWS credential chain (env → shared file →
//     IRSA / EC2 instance profile / ECS task role). Use this in EKS/ECS/EC2
//     so credentials rotate automatically and never live as long-lived
//     secrets in your config.
//
// Mixing (one set, one empty) is rejected — that almost always signals a
// misconfiguration where the operator forgot one half of the pair.
func NewS3Client(endpoint, accessKey, secretKey string, useSSL bool) (*S3Client, error) {
	return NewS3ClientWithPublicEndpoint(endpoint, "", accessKey, secretKey, useSSL)
}

// NewS3ClientWithPublicEndpoint is the full-shape constructor.
//
//   internalEndpoint: address the storage service uses for direct ops.
//   publicEndpoint:   address presigned URLs are signed against; "" =
//                     same as internal (production default).
//   useSSL: applies to BOTH endpoints. If your public endpoint is
//           HTTPS but internal is HTTP (or vice versa), call NewS3Client
//           and call SetPresignClient yourself. We chose not to add a
//           second SSL flag to keep the common signature small.
func NewS3ClientWithPublicEndpoint(internalEndpoint, publicEndpoint, accessKey, secretKey string, useSSL bool) (*S3Client, error) {
	creds, err := resolveCreds(accessKey, secretKey)
	if err != nil {
		return nil, err
	}
	internalHost := stripScheme(internalEndpoint)
	c, err := minio.New(internalHost, &minio.Options{
		Creds:  creds,
		Secure: useSSL,
		Region: "us-east-1",
	})
	if err != nil {
		return nil, fmt.Errorf("new minio client (internal): %w", err)
	}
	presign := c
	if publicEndpoint != "" {
		publicHost := stripScheme(publicEndpoint)
		if publicHost != internalHost {
			presign, err = minio.New(publicHost, &minio.Options{
				Creds:  creds,
				Secure: useSSL,
				// Critical: explicit region prevents PresignedPutObject
				// from making a GetBucketLocation call against the
				// public host — which the storage container can't
				// reach (it's "localhost:9000" from outside the
				// container; from inside, that's the container's own
				// loopback). Region is what GetBucketLocation would
				// have returned anyway, so skipping the call is safe.
				Region: "us-east-1",
			})
			if err != nil {
				return nil, fmt.Errorf("new minio client (presign): %w", err)
			}
		}
	}
	return &S3Client{c: c, presign: presign}, nil
}

// stripScheme tolerates "http://host:port" / "https://host:port" /
// trailing-slash inputs because operators set VAULTDMS_S3_ENDPOINT
// with full URLs in compose; minio.New wants bare "host:port".
func stripScheme(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.TrimRight(s, "/")
}

// resolveCreds picks between static and chain auth. Kept separate so tests
// can exercise the branching without spinning up a real client.
//
// minio-go's NewIAM("") covers the entire AWS credential chain in one
// helper: env vars → shared credentials file → EC2 instance metadata
// (IMDSv2) → ECS task role → EKS IRSA (via STS AssumeRoleWithWebIdentity
// when AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE are set). Empty endpoint
// argument means "auto-detect."
func resolveCreds(accessKey, secretKey string) (*credentials.Credentials, error) {
	hasKey := accessKey != ""
	hasSecret := secretKey != ""
	switch {
	case hasKey && hasSecret:
		return credentials.NewStaticV4(accessKey, secretKey, ""), nil
	case !hasKey && !hasSecret:
		return credentials.NewIAM(""), nil
	default:
		return nil, fmt.Errorf("s3 credentials misconfigured: supply both accessKey and secretKey, or neither (got accessKey=%t secretKey=%t)", hasKey, hasSecret)
	}
}

// PutObject uploads an object.
func (s *S3Client) PutObject(
	ctx context.Context,
	bucket, key string,
	reader io.Reader,
	size int64,
	contentType string,
) error {
	_, err := s.c.PutObject(ctx, bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("put object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// GetObject returns a reader for the object.
func (s *S3Client) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := s.c.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s/%s: %w", bucket, key, err)
	}
	// minio-go returns an error lazily on read — do a Stat to surface 404 now.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, err
	}
	return obj, nil
}

// DeleteObject removes an object. A missing object is not an error.
func (s *S3Client) DeleteObject(ctx context.Context, bucket, key string) error {
	if err := s.c.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
			return nil
		}
		return fmt.Errorf("remove object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// GeneratePresignedPutURL returns a URL the client can PUT to directly.
// Uses the presign client so the URL host matches what the browser
// will actually contact — Sig V4 stays valid.
func (s *S3Client) GeneratePresignedPutURL(
	ctx context.Context, bucket, key string, expiry time.Duration,
) (string, error) {
	u, err := s.presign.PresignedPutObject(ctx, bucket, key, expiry)
	return urlString(u), err
}

// GeneratePresignedGetURL returns a URL the client can GET from directly.
// Uses the presign client — see GeneratePresignedPutURL.
//
// `filename` is the human-readable name surfaced to the user (used in
// the Content-Disposition header). Empty string falls back to the
// last segment of the object key.
//
// `disposition` is "inline" or "attachment":
//   * inline     — the browser renders the response in place (PDF in an
//                  iframe, image in <img>, etc.). Default for previews.
//   * attachment — the browser triggers a Save-As dialog. Use for the
//                  explicit "Download" button.
//
// MinIO and S3 both honor the
// `response-content-disposition=...` query parameter; without it the
// browser falls back to its own MIME sniffing, which on some
// Windows/Chrome combos defaults to download for PDFs — exactly the
// "why is it auto-downloading" report we hit in dev.
func (s *S3Client) GeneratePresignedGetURL(
	ctx context.Context, bucket, key string, expiry time.Duration,
) (string, error) {
	return s.GeneratePresignedGetURLWith(ctx, bucket, key, expiry, "inline", "")
}

// GeneratePresignedGetURLWith is the variant that lets callers control
// the content-disposition + filename. The non-`With` form preserves
// the existing call sites and defaults to inline preview.
func (s *S3Client) GeneratePresignedGetURLWith(
	ctx context.Context, bucket, key string, expiry time.Duration,
	disposition, filename string,
) (string, error) {
	if disposition == "" {
		disposition = "inline"
	}
	if filename == "" {
		// Fall back to the last segment of the key. Already safe for
		// header use because content-addressable keys are URL-safe.
		filename = key
		if i := strings.LastIndex(filename, "/"); i >= 0 {
			filename = filename[i+1:]
		}
	}
	q := url.Values{}
	q.Set("response-content-disposition",
		fmt.Sprintf(`%s; filename="%s"`, disposition, filename))
	u, err := s.presign.PresignedGetObject(ctx, bucket, key, expiry, q)
	return urlString(u), err
}

// BucketExists reports whether a bucket exists.
func (s *S3Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	return s.c.BucketExists(ctx, bucket)
}

// CreateBucket makes a bucket if not already present.
func (s *S3Client) CreateBucket(ctx context.Context, bucket, region string) error {
	exists, err := s.c.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("bucket exists: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		return fmt.Errorf("make bucket %s: %w", bucket, err)
	}
	return nil
}

// CopyObject server-side copies an object.
func (s *S3Client) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	_, err := s.c.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: dstBucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: srcBucket, Object: srcKey},
	)
	if err != nil {
		return fmt.Errorf("copy %s/%s -> %s/%s: %w", srcBucket, srcKey, dstBucket, dstKey, err)
	}
	return nil
}

// GetObjectInfo returns metadata for an object.
func (s *S3Client) GetObjectInfo(ctx context.Context, bucket, key string) (ObjectInfo, error) {
	st, err := s.c.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat %s/%s: %w", bucket, key, err)
	}
	return ObjectInfo{
		Size:         st.Size,
		ContentType:  st.ContentType,
		ETag:         st.ETag,
		LastModified: st.LastModified,
	}, nil
}

// Ping does a cheap liveness check against the endpoint.
func (s *S3Client) Ping(ctx context.Context, healthBucket string) error {
	_, err := s.c.BucketExists(ctx, healthBucket)
	return err
}

func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}
