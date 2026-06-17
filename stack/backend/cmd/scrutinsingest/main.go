// Command scrutinsingest bulk-loads Assemblee Nationale open-data scrutins
// (recorded votes) into the structured voting-record store. Point it at a
// directory of per-scrutin JSON files - the unzipped
// Scrutins.json.zip archive from
// https://data.assemblee-nationale.fr/static/openData/repository/{legislature}/loi/scrutins/Scrutins.json.zip
// (Etalab open license, attribution "Assemblee nationale -
// donnees.assemblee-nationale.fr") - and it parses each file into one row per
// deputy's recorded position and upserts via the voting store. It is periodic
// bulk ingest, not live-per-claim; the live voting adapter reads what it writes.
// Re-running is idempotent: each row is keyed by (person, scrutin).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/store/postgres"
	"github.com/verovec/truth-in-stream/backend/internal/votingrecord"
)

// errMissingDir is returned when no scrutin directory is supplied; the command
// cannot guess where the unzipped archive lives.
var errMissingDir = errors.New("scrutinsingest: -dir is required (directory of unzipped Scrutins.json files)")

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	dir := flag.String("dir", "", "directory of Assemblee Nationale scrutin JSON files (unzipped Scrutins.json.zip)")
	flag.Parse()

	if err := run(logger, *dir); err != nil {
		logger.Error("scrutinsingest failed", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, dir string) error {
	if dir == "" {
		flag.Usage()
		return errMissingDir
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	summary, err := votingrecord.IngestDir(ctx, store, dir)
	if err != nil {
		return err
	}

	logger.InfoContext(ctx, "scrutins ingest complete",
		slog.String("dir", dir),
		slog.Int("files", summary.Files),
		slog.Int("records", summary.Records),
	)
	return nil
}
