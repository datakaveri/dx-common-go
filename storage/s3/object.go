package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// PutObject uploads size bytes from body to key with the given content type.
func (c *Client) PutObject(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	in := &awss3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if size > 0 {
		in.ContentLength = aws.Int64(size)
	}
	if _, err := c.api.PutObject(ctx, in); err != nil {
		return fmt.Errorf("s3.PutObject %q: %w", key, err)
	}
	return nil
}

// GetObject streams the object at key. The caller must close the reader.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, *ObjectInfo, error) {
	return c.getObject(ctx, key, "")
}

// GetObjectRange streams length bytes starting at offset. A non-positive length
// reads to the end of the object.
func (c *Client) GetObjectRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, *ObjectInfo, error) {
	var rng string
	if length > 0 {
		rng = fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	} else {
		rng = fmt.Sprintf("bytes=%d-", offset)
	}
	return c.getObject(ctx, key, rng)
}

func (c *Client) getObject(ctx context.Context, key, rng string) (io.ReadCloser, *ObjectInfo, error) {
	in := &awss3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := c.api.GetObject(ctx, in)
	if err != nil {
		return nil, nil, fmt.Errorf("s3.GetObject %q: %w", key, err)
	}
	info := &ObjectInfo{
		Key:          key,
		ETag:         aws.ToString(out.ETag),
		ContentType:  aws.ToString(out.ContentType),
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
	}
	return out.Body, info, nil
}

// HeadObject returns an object's metadata without its body.
func (c *Client) HeadObject(ctx context.Context, key string) (*ObjectInfo, error) {
	out, err := c.api.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3.HeadObject %q: %w", key, err)
	}
	return &ObjectInfo{
		Key:          key,
		ETag:         aws.ToString(out.ETag),
		ContentType:  aws.ToString(out.ContentType),
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
	}, nil
}

// ObjectExists reports whether key exists.
func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	_, err := c.api.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return false, nil
		}
		return false, fmt.Errorf("s3.ObjectExists %q: %w", key, err)
	}
	return true, nil
}

// DeleteObject removes key (no error if absent).
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	if _, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("s3.DeleteObject %q: %w", key, err)
	}
	return nil
}

// CopyObject copies srcKey to dstKey within the bucket.
func (c *Client) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	if _, err := c.api.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		CopySource: aws.String(c.bucket + "/" + srcKey),
		Key:        aws.String(dstKey),
	}); err != nil {
		return fmt.Errorf("s3.CopyObject %q -> %q: %w", srcKey, dstKey, err)
	}
	return nil
}

// IsNotFound reports whether err is the backend saying the object (or key) does
// not exist — HeadObject's NotFound or GetObject's NoSuchKey — as opposed to
// any failure to find out.
func IsNotFound(err error) bool {
	var nf *types.NotFound
	var nsk *types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nsk)
}
