package s3

import (
	"context"
	"io"
	"time"
)

// ObjectInfo is the metadata of a stored object.
type ObjectInfo struct {
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
}

// CompletedPart identifies one finished part of a multipart upload.
type CompletedPart struct {
	PartNumber int32
	ETag       string
}

// MultipartUpload describes an in-progress multipart upload.
type MultipartUpload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// ObjectStore is the object-storage contract implemented by *Client for any
// S3-compatible backend (AWS S3, MinIO). Services depend on this interface so
// they can be tested against a fake; the concrete *Client is returned by
// NewClient. Keys are opaque to the store — layout/naming is the caller's
// concern.
type ObjectStore interface {
	Reader
	Writer
	Presigner
	Multipart

	// Bucket returns the bucket every operation targets.
	Bucket() string
}

// Reader is the read side of an object store.
type Reader interface {
	// GetObject streams the object at key. The caller must close the reader.
	GetObject(ctx context.Context, key string) (io.ReadCloser, *ObjectInfo, error)
	// GetObjectRange streams length bytes starting at offset (an HTTP Range
	// request), so a preview never pulls a large object fully into memory. A
	// non-positive length reads to the end of the object.
	GetObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, *ObjectInfo, error)
	// HeadObject returns an object's metadata without its body.
	HeadObject(ctx context.Context, key string) (*ObjectInfo, error)
	// ObjectExists reports whether key exists.
	ObjectExists(ctx context.Context, key string) (bool, error)
	// ListObjects lists objects under prefix. By default it recurses; pass
	// WithDelimiter("/") for a single hierarchy level.
	ListObjects(ctx context.Context, prefix string, opts ...ListOption) ([]ObjectInfo, error)
	// ListLevel lists ONE hierarchy level under prefix: the objects directly
	// beneath it and the sub-prefixes ("folders") delimiter groups them into.
	// ListObjects with WithDelimiter returns only the former, which is enough
	// to page files but not to render a folder tree.
	ListLevel(ctx context.Context, prefix, delimiter string) ([]ObjectInfo, []string, error)
}

// Writer is the write side of an object store.
type Writer interface {
	// PutObject uploads size bytes from body to key with the given content type.
	PutObject(ctx context.Context, key, contentType string, body io.Reader, size int64) error
	// DeleteObject removes key (no error if absent).
	DeleteObject(ctx context.Context, key string) error
	// CopyObject copies srcKey to dstKey within the bucket.
	CopyObject(ctx context.Context, srcKey, dstKey string) error
}

// Presigner issues time-limited URLs a client can use directly against the
// backend without service-held credentials.
type Presigner interface {
	PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	PresignPutURL(ctx context.Context, key, contentType string, expiry time.Duration) (string, error)
	// PresignUploadPart issues a URL for uploading one part of an in-progress
	// multipart upload — the browser-driven multipart path.
	PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int32, expiry time.Duration) (string, error)
}

// Multipart covers the server-coordinated multipart-upload lifecycle.
type Multipart interface {
	InitiateMultipartUpload(ctx context.Context, key, contentType string) (uploadID string, err error)
	UploadPart(ctx context.Context, key, uploadID string, partNumber int32, body io.Reader, size int64) (etag string, err error)
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) error
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
	// ListMultipartUploads returns all in-progress multipart uploads (used to
	// reap orphaned uploads).
	ListMultipartUploads(ctx context.Context) ([]MultipartUpload, error)
}

var _ ObjectStore = (*Client)(nil)
