package s3client

import (
	"errors"
	"os"
)

// Credentials holds AWS credentials for S3 access.
type Credentials struct {
	AwsAccessKeyId     string
	AwsSecretAccessKey string
	AwsSessionToken    string // Optional, used for session-based credentials
	AwsRegion          string
}

// Validate checks that required credentials are present.
func (c *Credentials) Validate() error {
	if c.AwsAccessKeyId == "" {
		return errors.New("AWS access key ID is required")
	}
	if c.AwsSecretAccessKey == "" {
		return errors.New("AWS secret access key is required")
	}
	if c.AwsRegion == "" {
		return errors.New("AWS region is required")
	}
	return nil
}

// LoadFromEnv loads credentials from environment variables.
// It looks for:
//   - AWS_ACCESS_KEY_ID
//   - AWS_SECRET_ACCESS_KEY
//   - AWS_SESSION_TOKEN (optional)
//   - AWS_REGION or AWS_DEFAULT_REGION
func LoadFromEnv() Credentials {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	return Credentials{
		AwsAccessKeyId:     os.Getenv("AWS_ACCESS_KEY_ID"),
		AwsSecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		AwsSessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
		AwsRegion:          region,
	}
}
