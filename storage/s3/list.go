package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListOption configures ListObjects.
type ListOption func(*listOptions)

type listOptions struct {
	delimiter string
}

// WithDelimiter restricts a listing to a single hierarchy level (typically
// "/"), so it returns the immediate children of prefix rather than recursing.
func WithDelimiter(delimiter string) ListOption {
	return func(o *listOptions) { o.delimiter = delimiter }
}

// ListObjects lists objects under prefix, paginating internally. By default it
// recurses; pass WithDelimiter("/") for one level.
func (c *Client) ListObjects(ctx context.Context, prefix string, opts ...ListOption) ([]ObjectInfo, error) {
	var o listOptions
	for _, opt := range opts {
		opt(&o)
	}

	in := &awss3.ListObjectsV2Input{Bucket: aws.String(c.bucket)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}
	if o.delimiter != "" {
		in.Delimiter = aws.String(o.delimiter)
	}

	var objects []ObjectInfo
	paginator := awss3.NewListObjectsV2Paginator(c.api, in)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3.ListObjects %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			objects = append(objects, ObjectInfo{
				Key:          aws.ToString(obj.Key),
				ETag:         aws.ToString(obj.ETag),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
	}
	return objects, nil
}

// ListLevel lists one hierarchy level under prefix, paginating internally:
// the objects directly beneath it and the common prefixes delimiter groups the
// rest into. An empty delimiter defaults to "/".
func (c *Client) ListLevel(ctx context.Context, prefix, delimiter string) ([]ObjectInfo, []string, error) {
	if delimiter == "" {
		delimiter = "/"
	}
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(c.bucket), Delimiter: aws.String(delimiter)}
	if prefix != "" {
		in.Prefix = aws.String(prefix)
	}

	var (
		objects  []ObjectInfo
		prefixes []string
	)
	paginator := awss3.NewListObjectsV2Paginator(c.api, in)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("s3.ListLevel %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			objects = append(objects, ObjectInfo{
				Key:          aws.ToString(obj.Key),
				ETag:         aws.ToString(obj.ETag),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
		for _, cp := range page.CommonPrefixes {
			prefixes = append(prefixes, aws.ToString(cp.Prefix))
		}
	}
	return objects, prefixes, nil
}
