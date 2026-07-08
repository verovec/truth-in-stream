package domain

import "context"

// PresignedRequest is a pre-authorized HTTP request the browser executes
// directly against object storage. URL carries the signature and expiry; the
// caller MUST replay every header in SignedHeaders on the actual request or
// the signature check fails. It holds no net/http types so the storage port
// stays free of transport concerns.
type PresignedRequest struct {
	URL           string
	Method        string
	SignedHeaders map[string][]string
}

// MediaStore is the port for object storage of uploaded media. The concrete
// implementation (internal/storage) wraps S3/MinIO behind it so handlers and
// services depend on this interface, never the AWS SDK. Uploads and downloads
// go direct from the browser to storage via presigned URLs; the backend never
// proxies the bytes.
type MediaStore interface {
	// PresignUpload returns a presigned PUT request for key. The URL signs only
	// the host, so the uploader sets the object's Content-Type by sending that
	// header on the PUT; storage records whatever it sends.
	PresignUpload(ctx context.Context, key string) (PresignedRequest, error)
	// PresignDownload returns a presigned GET request for key.
	PresignDownload(ctx context.Context, key string) (PresignedRequest, error)
	// Exists reports whether an object with key is present.
	Exists(ctx context.Context, key string) (bool, error)
	// Delete removes the object with key. Deleting an absent key succeeds (S3
	// semantics), so callers can retry a partial deletion safely.
	Delete(ctx context.Context, key string) error
}
