package s3client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrPreconditionFailed is returned when a conditional request fails.
var ErrPreconditionFailed = errors.New("precondition failed")

// ErrObjectAlreadyExists is returned when trying to create an object that already exists.
var ErrObjectAlreadyExists = errors.New("object already exists")

// Client wraps the AWS S3 client with additional functionality.
type Client struct {
	raw      *s3.Client
	endpoint string
}

// NewClient creates a new S3 client based on the provided credentials and endpoint.
func NewClient(ctx context.Context, creds Credentials, endpoint string) (*Client, error) {
	if err := creds.Validate(); err != nil {
		return nil, err
	}

	s3Cfg, err := config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			creds.AwsAccessKeyId,
			creds.AwsSecretAccessKey,
			creds.AwsSessionToken,
		)),
		config.WithRegion(creds.AwsRegion),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	rawClient := s3.NewFromConfig(s3Cfg, func(o *s3.Options) {
		configureEndpoint(o, endpoint)
	})

	return &Client{
		raw:      rawClient,
		endpoint: endpoint,
	}, nil
}

// configureEndpoint sets up the S3 client endpoint options.
// It handles both AWS S3 endpoints and S3-compatible endpoints (like MinIO).
func configureEndpoint(o *s3.Options, endpoint string) {
	if endpoint == "" {
		return
	}

	// Check if endpoint already has a scheme
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		o.BaseEndpoint = aws.String(endpoint)
	} else {
		// Default to HTTPS for AWS-style endpoints
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s", endpoint))
	}

	// Use path-style addressing for non-AWS endpoints (e.g., MinIO, localhost)
	// AWS S3 uses virtual-hosted style (bucket.s3.region.amazonaws.com)
	// but S3-compatible services often require path-style (endpoint/bucket)
	if !strings.Contains(endpoint, "amazonaws.com") {
		o.UsePathStyle = true
	}
}

// PutObject uploads data to S3.
func (c *Client) PutObject(ctx context.Context, bucket, key string, data []byte) error {
	_, err := c.raw.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("failed to put object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// PutObjectThrottled uploads data to S3 with bandwidth limiting.
// bytesPerSec controls the upload speed.
func (c *Client) PutObjectThrottled(ctx context.Context, bucket, key string, data []byte, bytesPerSec int) error {
	throttledReader := NewThrottledReader(ctx, data, bytesPerSec)
	_, err := c.raw.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          throttledReader,
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("failed to put object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// HeadObject retrieves object metadata without downloading the body.
// Returns the ETag and content length.
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (etag string, contentLength int64, err error) {
	output, err := c.raw.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", 0, fmt.Errorf("failed to head object %s/%s: %w", bucket, key, err)
	}

	if output.ETag != nil {
		etag = *output.ETag
	}
	if output.ContentLength != nil {
		contentLength = *output.ContentLength
	}

	return etag, contentLength, nil
}

// GetObject retrieves an object from S3 and returns its contents.
func (c *Client) GetObject(ctx context.Context, bucket, key string) ([]byte, error) {
	output, err := c.raw.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %s/%s: %w", bucket, key, err)
	}
	defer output.Body.Close()

	data, err := io.ReadAll(output.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read object body: %w", err)
	}

	return data, nil
}

// GetRawClient returns the underlying AWS S3 client for advanced operations.
func (c *Client) GetRawClient() *s3.Client {
	return c.raw
}

// DeleteObject deletes an object from S3.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := c.raw.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// ObjectExists checks if an object exists in S3.
func (c *Client) ObjectExists(ctx context.Context, bucket, key string) (bool, error) {
	_, _, err := c.HeadObject(ctx, bucket, key)
	if err != nil {
		// Check if it's a "not found" error
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" {
				return false, nil
			}
		}
		// Also check for 404 in the error message (for wrapped errors)
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// PutObjectIfMatch uploads data to S3 only if the object's current ETag matches.
// Returns ErrPreconditionFailed if the ETag doesn't match.
func (c *Client) PutObjectIfMatch(ctx context.Context, bucket, key string, data []byte, expectedETag string) error {
	_, err := c.raw.PutObject(ctx, &s3.PutObjectInput{
		Bucket:  aws.String(bucket),
		Key:     aws.String(key),
		Body:    bytes.NewReader(data),
		IfMatch: aws.String(expectedETag),
	})
	if err != nil {
		// Check for precondition failed
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if apiErr.ErrorCode() == "PreconditionFailed" {
				return ErrPreconditionFailed
			}
		}
		return fmt.Errorf("failed to put object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// PutObjectIfNoneMatch uploads data to S3 only if no object exists at the key.
// Returns ErrObjectAlreadyExists if an object already exists.
func (c *Client) PutObjectIfNoneMatch(ctx context.Context, bucket, key string, data []byte) error {
	_, err := c.raw.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		// Check for precondition failed (object already exists)
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			if apiErr.ErrorCode() == "PreconditionFailed" {
				return ErrObjectAlreadyExists
			}
		}
		return fmt.Errorf("failed to put object %s/%s: %w", bucket, key, err)
	}
	return nil
}

// ObjectInfo represents metadata about an S3 object.
type ObjectInfo struct {
	Key          string
	ETag         string
	Size         int64
	LastModified string
}

// ListObjects lists objects in a bucket with an optional prefix.
// Returns a slice of ObjectInfo with key, ETag, size, and last modified time.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	paginator := s3.NewListObjectsV2Paginator(c.raw, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects in %s/%s: %w", bucket, prefix, err)
		}

		for _, obj := range page.Contents {
			info := ObjectInfo{}
			if obj.Key != nil {
				info.Key = *obj.Key
			}
			if obj.ETag != nil {
				info.ETag = *obj.ETag
			}
			if obj.Size != nil {
				info.Size = *obj.Size
			}
			if obj.LastModified != nil {
				info.LastModified = obj.LastModified.Format("2006-01-02T15:04:05Z")
			}
			objects = append(objects, info)
		}
	}

	return objects, nil
}

// MultipartUploadPart represents a part in a multipart upload.
type MultipartUploadPart struct {
	PartNumber int32
	Data       []byte
}

// MultipartUpload performs a multipart upload with the given parts.
// Parts can be uploaded in any order, but part numbers determine the final order.
func (c *Client) MultipartUpload(ctx context.Context, bucket, key string, parts []MultipartUploadPart) error {
	// Step 1: Initiate multipart upload
	createOutput, err := c.raw.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to create multipart upload: %w", err)
	}
	uploadID := createOutput.UploadId

	// Step 2: Upload parts
	var completedParts []types.CompletedPart
	for _, part := range parts {
		uploadOutput, err := c.raw.UploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			PartNumber: aws.Int32(part.PartNumber),
			UploadId:   uploadID,
			Body:       bytes.NewReader(part.Data),
		})
		if err != nil {
			// Abort the upload on failure
			_, _ = c.raw.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(bucket),
				Key:      aws.String(key),
				UploadId: uploadID,
			})
			return fmt.Errorf("failed to upload part %d: %w", part.PartNumber, err)
		}
		completedParts = append(completedParts, types.CompletedPart{
			PartNumber: aws.Int32(part.PartNumber),
			ETag:       uploadOutput.ETag,
		})
	}

	// Step 3: Complete multipart upload
	// Parts must be sorted by part number for the complete call
	sortedParts := make([]types.CompletedPart, len(completedParts))
	copy(sortedParts, completedParts)
	// Sort by part number
	for i := 0; i < len(sortedParts)-1; i++ {
		for j := i + 1; j < len(sortedParts); j++ {
			if *sortedParts[i].PartNumber > *sortedParts[j].PartNumber {
				sortedParts[i], sortedParts[j] = sortedParts[j], sortedParts[i]
			}
		}
	}

	_, err = c.raw.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: sortedParts,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to complete multipart upload: %w", err)
	}

	return nil
}
