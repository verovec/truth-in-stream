package handler

import (
	"errors"
	"net/http"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/export"
	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// noSnapshotMessage tells an operator why an export is empty and how to fix
// it: the replayer serves the durable store first and the replay cache second,
// so a miss means the video has no stored pre-analysis and no un-expired
// cached session, and running an analysis makes the exports available.
const noSnapshotMessage = "no stored or cached analysis for this video; run an analysis to make exports available"

// renderExport reads the named formatter's bytes for a video and streams them as a
// download attachment. It is the shared body of both export handlers: it resolves
// the video for its filename, loads the cached snapshot through the replayer, and
// never triggers transcription or an LLM call. A missing video or missing snapshot
// is a 404; a disabled replay cache is a 503; the handler owns transport only.
func renderExport(
	videos VideoService,
	replayer AnalysisReplayer,
	contentType, extension string,
	render func(events []service.LiveEvent) []byte,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if replayer == nil {
			httpx.Error(w, http.StatusServiceUnavailable, "analysis cache is not configured")
			return
		}
		id := r.PathValue("id")
		playable, err := videos.Get(r.Context(), id)
		switch {
		case errors.Is(err, domain.ErrVideoNotFound):
			httpx.Error(w, http.StatusNotFound, "unknown video")
			return
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		}

		events, found, err := replayer.Snapshot(r.Context(), id)
		switch {
		case err != nil:
			httpx.Error(w, http.StatusInternalServerError, "internal error")
			return
		case !found:
			httpx.Error(w, http.StatusNotFound, noSnapshotMessage)
			return
		}

		filename := exportFilename(playable.Video, extension)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(render(events))
	}
}

// exportTranscriptHandler streams the SRT subtitle track of a video's transcript.
func exportTranscriptHandler(videos VideoService, replayer AnalysisReplayer) http.HandlerFunc {
	return renderExport(videos, replayer, "application/x-subrip; charset=utf-8", ".srt", export.SRT)
}

// exportClaimsHandler streams the CSV decision trace of a video's fact-check.
func exportClaimsHandler(videos VideoService, replayer AnalysisReplayer) http.HandlerFunc {
	return renderExport(videos, replayer, "text/csv; charset=utf-8", ".csv", export.CSV)
}

// exportFilename builds a safe download filename from the video's title, falling
// back to its id, and appends the format extension. The name is ASCII-folded and
// reduced to lowercase alphanumerics joined by single hyphens so it is portable
// across operating systems and carries no quote, path separator, or non-ASCII byte
// that would break the plain Content-Disposition filename parameter.
func exportFilename(video domain.Video, extension string) string {
	base := slugify(video.Title)
	if base == "" {
		base = slugify(video.ID)
	}
	if base == "" {
		base = "export"
	}
	return base + extension
}

// slugify folds Unicode accents to ASCII (decomposing then dropping combining
// marks, so "Débat" becomes "debat") and keeps only lowercase ASCII letters and
// digits, collapsing every other run to a single hyphen.
func slugify(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			// A combining mark left by NFD decomposition; drop it so the base
			// letter survives as plain ASCII.
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		case !lastHyphen && b.Len() > 0:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}
