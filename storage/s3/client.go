package s3

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Client implements StorageRepository using the AWS SDK v2.
type S3Client struct {
	client  *awss3.Client
	presign *awss3.PresignClient
	bucket  string
}

// NewClient creates an S3Client from Config. For MinIO, set Provider to "minio"
// and supply Endpoint / ForcePathStyle = true.
func NewClient(cfg Config) (*S3Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		),
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3.NewClient: load config: %w", err)
	}

	s3Opts := []func(*awss3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *awss3.Options) {
			scheme := "https"
			if !cfg.UseSSL {
				scheme = "http"
			}
			o.BaseEndpoint = aws.String(scheme + "://" + cfg.Endpoint)
		})
	}
	if cfg.ForcePathStyle {
		s3Opts = append(s3Opts, func(o *awss3.Options) {
			o.UsePathStyle = true
		})
	}

	client := awss3.NewFromConfig(awsCfg, s3Opts...)
	presign := awss3.NewPresignClient(client)

	return &S3Client{
		client:  client,
		presign: presign,
		bucket:  cfg.Bucket,
	}, nil
}

// PutObject uploads body to the configured bucket.
func (c *S3Client) PutObject(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	_, err := c.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
		Body:          body,
	})
	if err != nil {
		return fmt.Errorf("s3.PutObject: %w", err)
	}
	return nil
}

// GetObject downloads the object and returns a ReadCloser and its metadata.
func (c *S3Client) GetObject(ctx context.Context, key string) (io.ReadCloser, *ObjectInfo, error) {
	out, err := c.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("s3.GetObject: %w", err)
	}

	info := &ObjectInfo{
		Key:  key,
		ETag: aws.ToString(out.ETag),
	}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		info.ContentType = *out.ContentType
	}
	if out.LastModified != nil {
		info.LastModified = *out.LastModified
	}

	return out.Body, info, nil
}

// DeleteObject removes the object from the bucket.
func (c *S3Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3.DeleteObject: %w", err)
	}
	return nil
}

// ListObjects returns object metadata for all keys with the given prefix.
func (c *S3Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo
	paginator := awss3.NewListObjectsV2Paginator(c.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3.ListObjects: %w", err)
		}
		for _, obj := range page.Contents {
			info := ObjectInfo{
				Key:  aws.ToString(obj.Key),
				ETag: aws.ToString(obj.ETag),
			}
			if obj.Size != nil {
				info.Size = *obj.Size
			}
			if obj.LastModified != nil {
				info.LastModified = *obj.LastModified
			}
			objects = append(objects, info)
		}
	}
	return objects, nil
}

// PresignGetURL generates a pre-signed URL for downloading an object.
func (c *S3Client) PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	req, err := c.presign.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, awss3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3.PresignGetURL: %w", err)
	}
	return req.URL, nil
}

// PresignPutURL generates a pre-signed URL for uploading an object.
func (c *S3Client) PresignPutURL(ctx context.Context, key, contentType string, expiry time.Duration) (string, error) {
	req, err := c.presign.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, awss3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("s3.PresignPutURL: %w", err)
	}
	return req.URL, nil
}

// InitiateMultipartUpload starts a multipart upload session.
func (c *S3Client) InitiateMultipartUpload(ctx context.Context, key, contentType string) (string, error) {
	out, err := c.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("s3.InitiateMultipartUpload: %w", err)
	}
	return aws.ToString(out.UploadId), nil
}

// UploadPart uploads one part of a multipart upload and returns its ETag.
func (c *S3Client) UploadPart(ctx context.Context, key, uploadID string, partNumber int32, body io.Reader, size int64) (string, error) {
	out, err := c.client.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(partNumber),
		ContentLength: aws.Int64(size),
		Body:          body,
	})
	if err != nil {
		return "", fmt.Errorf("s3.UploadPart part %d: %w", partNumber, err)
	}
	return aws.ToString(out.ETag), nil
}

// CompleteMultipartUpload finalises a multipart upload.
func (c *S3Client) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, p := range parts {
		completed = append(completed, types.CompletedPart{
			PartNumber: aws.Int32(p.PartNumber),
			ETag:       aws.String(p.ETag),
		})
	}

	_, err := c.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket:   aws.String(c.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completed,
		},
	})
	if err != nil {
		return fmt.Errorf("s3.CompleteMultipartUpload: %w", err)
	}
	return nil
}

// AbortMultipartUpload cancels an in-progress multipart upload.
func (c *S3Client) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	_, err := c.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket:   aws.String(c.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("s3.AbortMultipartUpload: %w", err)
	}
	return nil
}
