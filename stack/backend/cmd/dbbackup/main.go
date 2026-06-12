// Command dbbackup runs a single pg_dump of the database addressed by
// DATABASE_URL and uploads the custom-format archive to the S3 bucket named by
// DB_BACKUP_BUCKET. It is a one-shot job: the cloud schedule (EventBridge
// Scheduler -> Fargate, see stack/terraform/modules/scheduled-task) runs it on a
// cron so backups happen without a human running `make backup`. The dump uses
// the same flags as scripts/db-backup.sh (internal/dbbackup.DumpArgs) and lands
// under the same key convention, so a scheduled dump and a manual one are
// interchangeable and `make restore` consumes either.
//
// Configuration is read straight from the environment, the minimal surface a
// scheduled task needs:
//   - DATABASE_URL     (required) the database DSN, injected from Secrets Manager
//   - DB_BACKUP_BUCKET (required) the destination S3 bucket
//   - DB_BACKUP_PREFIX (optional) key prefix; defaults to db-backups
//   - DB_NAME          (optional) overrides the dump name; default is derived
//     from the DSN
//   - AWS_REGION       (optional) overrides the SDK's default region resolution
//
// Credentials come from the SDK default chain (the ECS task role in
// production); the DSN is never logged.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/dbbackup"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("database backup failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	dsn, err := requireEnv("DATABASE_URL")
	if err != nil {
		return err
	}
	bucket, err := requireEnv("DB_BACKUP_BUCKET")
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Real S3: no endpoint, no static credentials (the task role supplies them).
	client, err := dbbackup.NewS3Client(ctx, dbbackup.S3Config{Region: os.Getenv("AWS_REGION")})
	if err != nil {
		return err
	}
	uploader := dbbackup.NewS3Uploader(client, bucket)

	logger.InfoContext(ctx, "starting database backup", slog.String("bucket", bucket))
	key, err := dbbackup.Backup(ctx, dbbackup.PgDump, uploader, dbbackup.Options{
		DSN:    dsn,
		Prefix: os.Getenv("DB_BACKUP_PREFIX"),
		DBName: os.Getenv("DB_NAME"),
		Now:    time.Now(),
	})
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "database backup uploaded",
		slog.String("bucket", bucket),
		slog.String("key", key))
	return nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", errors.New(key + " is required")
	}
	return v, nil
}
