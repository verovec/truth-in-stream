package dbbackup

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config selects the object-storage backend the backup is uploaded to. An
// empty Endpoint targets real AWS S3 and resolves credentials through the
// default chain (the ECS task role in production); a non-empty Endpoint with
// static credentials and UsePathStyle targets a MinIO container, the same split
// the media storage adapter makes.
type S3Config struct {
	Bucket       string
	Region       string
	Endpoint     string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
}

// s3Uploader is the production Uploader: it writes the dump as a single object
// with a known content length.
type s3Uploader struct {
	client *s3.Client
	bucket string
}

func (u *s3Uploader) Upload(ctx context.Context, key string, body io.Reader, size int64) error {
	_, err := u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(u.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String("application/octet-stream"),
	})
	if err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

// NewS3Client builds the S3 client the backup uploads through. Region, when
// set, overrides the SDK's default resolution; static credentials are used only
// when both are supplied, otherwise the default chain (the ECS task role) is
// used; a non-empty Endpoint and UsePathStyle target a MinIO container. The
// request-checksum mode matches the media storage adapter so MinIO, which
// rejects the SDK's default aws-chunked trailers, is also addressable.
func NewS3Client(ctx context.Context, cfg S3Config) (*s3.Client, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}), nil
}

// NewS3Uploader adapts an S3 client to Uploader, writing dumps to bucket.
func NewS3Uploader(client *s3.Client, bucket string) Uploader {
	return &s3Uploader{client: client, bucket: bucket}
}
