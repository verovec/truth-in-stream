package tvcapture

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// remuxFunc remuxes the TS file at tsPath into the MP4 at mp4Path. The real
// implementation runs ffmpeg -c copy; tests inject a fake that writes a stub.
type remuxFunc func(ctx context.Context, tsPath, mp4Path string) error

// recordingUploader is the subset of the backend client the archiver needs to
// register a completed recording.
type recordingUploader interface {
	RequestUpload(ctx context.Context, channelID string, recordedAt time.Time, sizeBytes int64) (recordingTicket, error)
	UploadFile(ctx context.Context, tk recordingTicket, path string) error
	Register(ctx context.Context, videoID string) error
}

// archiver turns completed TS segments into uploaded, registered recordings.
type archiver struct {
	uploader   recordingUploader
	remux      remuxFunc
	ffmpegPath string
	logger     *slog.Logger
}

func newArchiver(uploader recordingUploader, ffmpegPath string, logger *slog.Logger) *archiver {
	a := &archiver{uploader: uploader, ffmpegPath: ffmpegPath, logger: logger}
	a.remux = a.ffmpegRemux
	return a
}

// ffmpegRemux is the real remux: stream-copy TS to a faststart MP4.
func (a *archiver) ffmpegRemux(ctx context.Context, tsPath, mp4Path string) error {
	cmd := exec.CommandContext(ctx, a.ffmpegPath, remuxArgs(tsPath, mp4Path)...)
	cmd.Env = utcEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tvcapture: remux %s: %w (%s)", filepath.Base(tsPath), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// archive remuxes one TS segment to MP4, uploads it via a presigned request,
// registers the recording, then deletes both local files. recorded_at is parsed
// from the segment filename (UTC).
func (a *archiver) archive(ctx context.Context, ch Channel, tsPath string) error {
	recordedAt, err := parseSegmentTime(tsPath)
	if err != nil {
		return err
	}
	mp4Path := strings.TrimSuffix(tsPath, filepath.Ext(tsPath)) + ".mp4"

	if err := a.remux(ctx, tsPath, mp4Path); err != nil {
		return err
	}

	info, err := os.Stat(mp4Path)
	if err != nil {
		return fmt.Errorf("tvcapture: stat remuxed recording: %w", err)
	}

	tk, err := a.uploader.RequestUpload(ctx, ch.ID, recordedAt, info.Size())
	if err != nil {
		return err
	}
	if err := a.uploader.UploadFile(ctx, tk, mp4Path); err != nil {
		return err
	}
	if err := a.uploader.Register(ctx, tk.VideoID); err != nil {
		return err
	}

	if err := os.Remove(tsPath); err != nil && !os.IsNotExist(err) {
		a.logger.Warn("tvcapture: remove ts segment", slog.String("slug", ch.Slug), slog.Any("err", err))
	}
	if err := os.Remove(mp4Path); err != nil && !os.IsNotExist(err) {
		a.logger.Warn("tvcapture: remove mp4 recording", slog.String("slug", ch.Slug), slog.Any("err", err))
	}
	a.logger.Info("tvcapture: archived recording",
		slog.String("slug", ch.Slug),
		slog.String("video_id", tk.VideoID),
		slog.Time("recorded_at", recordedAt),
		slog.Int64("size_bytes", info.Size()))
	return nil
}

// salvage archives every leftover TS segment already in dir, in filename order
// (chronological). A single segment's failure is logged and skipped so one bad
// file cannot strand the rest; salvage returns nil (best-effort recovery).
func (a *archiver) salvage(ctx context.Context, ch Channel, dir string) error {
	entries, err := filepath.Glob(filepath.Join(dir, "*"+segmentExt))
	if err != nil {
		return fmt.Errorf("tvcapture: scan salvage dir: %w", err)
	}
	sort.Strings(entries)
	for _, ts := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.archive(ctx, ch, ts); err != nil {
			a.logger.Warn("tvcapture: salvage segment failed",
				slog.String("slug", ch.Slug),
				slog.String("segment", filepath.Base(ts)),
				slog.Any("err", err))
			continue
		}
	}
	return nil
}
