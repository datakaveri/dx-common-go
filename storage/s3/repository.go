package s3

import (
	"context"
	"io"
	"time"
)

// ObjectInfo holds metadata about an object in the bucket.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
}

// CompletedPart carries the ETag and part number for a multipart upload.
type CompletedPart struct {
	PartNumber int32
	ETag       string
}

// StorageRepository defines all storage operations expected of an S3-compatible
// backend (AWS S3 or MinIO).
type StorageRepository interface {
	// PutObject uploads body to bucket/key with the given content type.
	PutObject(ctx context.Context, key, contentType string, body io.Reader, size int64) error

	// GetObject downloads the object at key. The caller is responsible for closing
	// the returned ReadCloser.
	GetObject(ctx context.Context, key string) (io.ReadCloser, *ObjectInfo, error)

	// DeleteObject removes the object at key.
	DeleteObject(ctx context.Context, key string) error

	// ListObjects returns all objects whose keys share the given prefix.
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)

	// PresignGetURL generates a time-limited pre-signed URL for GET.
	PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)

	// PresignPutURL generates a time-limited pre-signed URL for PUT.
	PresignPutURL(ctx context.Context, key, contentType string, expiry time.Duration) (string, error)

	// InitiateMultipartUpload starts a multipart upload and returns the uploadID.
	InitiateMultipartUpload(ctx context.Context, key, contentType string) (string, error)

	// UploadPart uploads a single part and returns its ETag.
	UploadPart(ctx context.Context, key, uploadID string, partNumber int32, body io.Reader, size int64) (string, error)

	// CompleteMultipartUpload assembles all uploaded parts.
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) error

	// AbortMultipartUpload cancels an in-progress multipart upload.
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
}
