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
type S3Client struct {
	c *minio.Client
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
	creds, err := resolveCreds(accessKey, secretKey)
	if err != nil {
		return nil, err
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  creds,
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("new minio client: %w", err)
	}
	return &S3Client{c: c}, nil
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

// SetStorageClass moves an object to a different S3 storage class
// (e.g. STANDARD → STANDARD_IA → GLACIER) by issuing a server-side
// CopyObject onto itself with x-amz-storage-class set on the
// destination. This is the AWS-canonical way to transition existing
// objects without re-uploading. MinIO mirrors the API but treats the
// class as metadata only — the bytes don't move tiers locally, which
// is acceptable for dev/test (audit trail still records the
// transition; bill-impact only matters in production AWS).
//
// Returns nil if the object is already in the target class.
func (s *S3Client) SetStorageClass(ctx context.Context, bucket, key, storageClass string) error {
	src := minio.CopySrcOptions{Bucket: bucket, Object: key}
	dst := minio.CopyDestOptions{
		Bucket:       bucket,
		Object:       key,
		UserMetadata: map[string]string{"x-amz-storage-class": storageClass},
		// x-amz-metadata-directive: REPLACE so the new storage class
		// takes effect; without it AWS preserves the source object's
		// metadata including its current storage class.
		ReplaceMetadata: true,
	}
	if _, err := s.c.CopyObject(ctx, dst, src); err != nil {
		return fmt.Errorf("set storage class %s on %s/%s: %w", storageClass, bucket, key, err)
	}
	return nil
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
func (s *S3Client) GeneratePresignedPutURL(
	ctx context.Context, bucket, key string, expiry time.Duration,
) (string, error) {
	u, err := s.c.PresignedPutObject(ctx, bucket, key, expiry)
	return urlString(u), err
}

// GeneratePresignedGetURL returns a URL the client can GET from directly.
func (s *S3Client) GeneratePresignedGetURL(
	ctx context.Context, bucket, key string, expiry time.Duration,
) (string, error) {
	u, err := s.c.PresignedGetObject(ctx, bucket, key, expiry, url.Values{})
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
