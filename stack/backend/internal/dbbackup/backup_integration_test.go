package dbbackup_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/go-cmp/cmp"

	"github.com/verovec/truth-in-stream/backend/internal/dbbackup"
)

// TestBackupUploadsRestorableDump is the end-to-end check behind the scheduled
// backup job: dbbackup.Backup runs the real pg_dump (dbbackup.PgDump), uploads
// the custom-format archive to object storage under the conventional key, and
// the uploaded object restores byte-for-byte, halfvec embeddings included. It is
// the full feature path - dump, upload, download, restore - exercised against a
// real database and a real S3-compatible store.
//
// It skips unless both TEST_DATABASE_URL and TEST_S3_ENDPOINT are set, mirroring
// the round-trip test's gating; CI provides Postgres but no MinIO, so it runs
// locally against the dev stack's object store.
func TestBackupUploadsRestorableDump(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping backup upload e2e")
	}
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set; skipping backup upload e2e")
	}
	pgRestore := lookTool(t, "pg_restore")
	lookTool(t, "pg_dump")

	bucket := getenv("TEST_S3_BUCKET", "db-backups-test")
	// One MinIO-targeted client built through the production NewS3Client, used
	// both for the upload under test (via the production NewS3Uploader) and for
	// the test's own bucket setup and download-to-verify, so the e2e exercises
	// the shipped upload path rather than a copy of it.
	client := minioClient(t, endpoint)
	ensureBucket(t, client, bucket)
	up := dbbackup.NewS3Uploader(client, bucket)

	ctx := t.Context()
	suffix := time.Now().UnixNano()
	srcDB := fmt.Sprintf("dbbackup_e2e_src_%d", suffix)
	dstDB := fmt.Sprintf("dbbackup_e2e_dst_%d", suffix)

	admin := connect(ctx, t, dsn)
	createDatabase(ctx, t, admin, srcDB)
	t.Cleanup(func() { dropDatabase(t, admin, srcDB) })
	createDatabase(ctx, t, admin, dstDB)
	t.Cleanup(func() { dropDatabase(t, admin, dstDB) })

	srcDSN := withDB(t, dsn, srcDB)
	dstDSN := withDB(t, dsn, dstDB)

	src := connect(ctx, t, srcDSN)
	mustExec(ctx, t, src, "CREATE EXTENSION IF NOT EXISTS vector")
	mustExec(ctx, t, src, "CREATE TABLE embeddings (id int PRIMARY KEY, embedding halfvec(3), label text)")
	mustExec(ctx, t, src, "INSERT INTO embeddings (id, embedding, label) VALUES (1, '[1,2.5,-0.5]', 'alpha'), (2, '[0.25,16,1024]', 'beta'), (3, '[-2,0,0.125]', 'gamma')")
	want := readEmbeddings(ctx, t, src)
	if len(want) != 3 {
		t.Fatalf("seed: expected 3 rows, got %d", len(want))
	}

	now := time.Date(2026, 6, 12, 4, 0, 0, 0, time.UTC)
	key, err := dbbackup.Backup(ctx, dbbackup.PgDump, up, dbbackup.Options{DSN: srcDSN, Now: now})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	wantKey := dbbackup.DumpKey(dbbackup.DefaultPrefix, srcDB, now)
	if key != wantKey {
		t.Errorf("uploaded key = %q, want %q", key, wantKey)
	}

	archive := filepath.Join(t.TempDir(), "downloaded.dump")
	download(ctx, t, client, bucket, key, archive)

	// pg_restore can exit non-zero for benign reasons; the data diff below is
	// the real gate, exactly as in the dump/restore round-trip test.
	logRun(ctx, t, pgRestore, dbbackup.RestoreArgs(dstDSN, archive))

	dst := connect(ctx, t, dstDSN)
	got := readEmbeddings(ctx, t, dst)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("embeddings differ after backup/restore (-source +restored):\n%s", diff)
	}
}

func minioClient(t *testing.T, endpoint string) *s3.Client {
	t.Helper()
	client, err := dbbackup.NewS3Client(t.Context(), dbbackup.S3Config{
		Region:       getenv("TEST_S3_REGION", "us-east-1"),
		Endpoint:     endpoint,
		AccessKey:    getenv("TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretKey:    getenv("TEST_S3_SECRET_KEY", "minioadmin"),
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("build minio client: %v", err)
	}
	return client
}

func ensureBucket(t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	_, err := client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return
	}
	// An already-owned bucket is the normal repeat-run case, not a failure.
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if errors.As(err, &owned) || errors.As(err, &exists) {
		return
	}
	t.Fatalf("create bucket %q: %v", bucket, err)
}

func download(ctx context.Context, t *testing.T, client *s3.Client, bucket, key, outPath string) {
	t.Helper()
	out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get object %q: %v", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	f, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %q: %v", outPath, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, out.Body); err != nil {
		t.Fatalf("download %q: %v", key, err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
