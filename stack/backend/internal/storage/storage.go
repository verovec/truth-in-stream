// Package storage is the object-storage adapter for uploaded media. It wraps
// the AWS SDK for S3 behind domain.MediaStore so the same code path serves a
// real S3 bucket on AWS and a MinIO container in local development, selected by
// configuration alone. It exposes no HTTP types.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// maxPresignTTL is the SigV4 ceiling for a presigned URL's validity (7 days).
// A longer expiry produces an unusable URL, so it is rejected at construction.
const maxPresignTTL = 7 * 24 * time.Hour

// Config selects the object-storage backend. An empty Endpoint targets real
// AWS S3 and resolves credentials through the default chain (the ECS task
// role in production); a non-empty Endpoint with static credentials and
// UsePathStyle targets a MinIO container in local development.
type Config struct {
	Endpoint     string
	Region       string
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
	PutTTL       time.Duration
	GetTTL       time.Duration
}

// S3Store is the S3/MinIO-backed implementation of domain.MediaStore.
type S3Store struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
	putTTL    time.Duration
	getTTL    time.Duration
}

var _ domain.MediaStore = (*S3Store)(nil)

// New builds an S3Store from cfg. It fails fast on a missing bucket or a
// presign TTL outside (0, 7 days]; everything else is delegated to the SDK's
// default configuration so AWS and MinIO share one construction path.
func New(ctx context.Context, cfg Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("storage: bucket is required")
	}
	// Static credentials are all-or-nothing: a half-set pair would silently
	// fall back to the default chain and hit the wrong account.
	if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
		return nil, errors.New("storage: access key and secret key must be set together")
	}
	if err := validateTTL("put", cfg.PutTTL); err != nil {
		return nil, err
	}
	if err := validateTTL("get", cfg.GetTTL); err != nil {
		return nil, err
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.UsePathStyle
		// MinIO rejects the SDK's default aws-chunked checksum trailers; only
		// compute a request checksum when the operation actually requires one.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})

	return &S3Store{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    cfg.Bucket,
		putTTL:    cfg.PutTTL,
		getTTL:    cfg.GetTTL,
	}, nil
}

// PresignUpload returns a presigned PUT request the browser uses to upload an
// object directly to storage without the backend proxying the bytes. The
// signed request covers only the host; the uploader sets the object's
// Content-Type by sending that header on the PUT.
func (s *S3Store) PresignUpload(ctx context.Context, key string) (domain.PresignedRequest, error) {
	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(s.putTTL))
	if err != nil {
		return domain.PresignedRequest{}, fmt.Errorf("storage: presign upload %q: %w", key, err)
	}
	return toPresigned(req), nil
}

// PresignDownload returns a presigned GET request the browser uses to stream an
// object directly from storage, including range requests for playback.
func (s *S3Store) PresignDownload(ctx context.Context, key string) (domain.PresignedRequest, error) {
	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(s.getTTL))
	if err != nil {
		return domain.PresignedRequest{}, fmt.Errorf("storage: presign download %q: %w", key, err)
	}
	return toPresigned(req), nil
}

// Upload writes body to key server-side. This is the path taken when the
// backend itself holds the bytes - a video downloaded from a YouTube link -
// rather than the browser pushing them through a presigned PUT. size is the
// exact content length so storage can stream a single object; contentType is
// recorded on the object for playback.
func (s *S3Store) Upload(ctx context.Context, key string, body io.Reader, contentType string, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
	})
	if err != nil {
		return fmt.Errorf("storage: put object %q: %w", key, err)
	}
	return nil
}

// Download opens key for server-side reading, the path that feeds a stored
// object back into the transcription pipeline. The caller owns the returned
// reader and MUST close it.
func (s *S3Store) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: get object %q: %w", key, err)
	}
	return out.Body, nil
}

// Exists reports whether key is present in the bucket. A missing object is not
// an error; any other failure is returned wrapped.
func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("storage: head object %q: %w", key, err)
	}
	return true, nil
}

// toPresigned converts the SDK's signed request to the transport-free domain
// type, copying the signed headers the caller must replay on the real request.
func toPresigned(req *v4.PresignedHTTPRequest) domain.PresignedRequest {
	headers := make(map[string][]string, len(req.SignedHeader))
	for k, v := range req.SignedHeader {
		// Clone so a caller mutating the returned headers cannot reach back
		// into the SDK's backing slices.
		headers[k] = slices.Clone(v)
	}
	return domain.PresignedRequest{
		URL:           req.URL,
		Method:        req.Method,
		SignedHeaders: headers,
	}
}

// isNotFound reports whether err is a HEAD-object "object missing" signal. The
// SDK maps any HeadObject 404 to *types.NotFound (including a bare-bodied 404
// from S3-compatible servers, where it derives the code from the status), so
// the typed check is exhaustive for this operation.
func isNotFound(err error) bool {
	_, ok := errors.AsType[*types.NotFound](err)
	return ok
}

func validateTTL(name string, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("storage: %s presign TTL must be positive, got %s", name, ttl)
	}
	if ttl > maxPresignTTL {
		return fmt.Errorf("storage: %s presign TTL %s exceeds the SigV4 maximum %s", name, ttl, maxPresignTTL)
	}
	return nil
}
