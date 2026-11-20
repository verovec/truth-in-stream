package handler

// Live fact-check streaming API.
//
//	GET /api/videos/{id}/live   (WebSocket) stream a video's audio, receive
//	                            incremental subtitles and fact-check results
//
// The client opens a WebSocket for a known video and streams its audio as raw
// 16 kHz mono PCM in binary frames, paced to playback. The server transcribes
// live, gates and matches each finalized statement, and pushes two JSON text
// frames per statement: a "subtitle" the moment the statement is transcribed,
// then a "result" once its verdict is ready. Both share an "id" so a verdict
// that lands after its subtitle reconciles to the right statement. A result
// frame carries the per-segment shape (start, end, text, matches, skip_reason).

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// LiveAnalyzer is the slice of the live analysis service the WebSocket handler
// drives: audio bytes in, fact-check events out. Satisfied by
// *service.LiveAnalyzer. The handler owns the socket; this port carries no
// transport types.
type LiveAnalyzer interface {
	Run(ctx context.Context, audio <-chan []byte) (<-chan service.LiveEvent, error)
}

// AnalysisRecorder persists a finite video's completed analysis so the cache-hit
// replay path can re-emit it without re-running the pipeline. The handler tees
// the live event stream and, only on a genuine completion (audio reached EOF and
// the pipeline drained, not an early disconnect), hands the ordered events here.
// Satisfied by *service.SnapshotPersister; the handler owns neither the snapshot
// wire format nor the cache contract. A nil recorder disables capture entirely,
// so the live route works unchanged when snapshot persistence is not wired.
type AnalysisRecorder interface {
	Persist(ctx context.Context, videoID string, events []service.LiveEvent) error
}

// AnalysisReplayer is the read side of the stored analyses the handler
// consults at session open: it returns a finite video's complete analysis so
// the handler can re-emit it instead of running transcription and the LLMs.
// found is false on a miss, a disabled store, an unsupported snapshot version,
// a corrupt payload, or a backend error - every degraded case collapses to a
// single fall-through to the live pipeline, and the returned error is reserved
// for a fault the caller should not absorb (the implementations log and
// degrade, so in practice it is always nil). Satisfied by
// *service.CompositeReplayer (durable Postgres pre-analyses first, then the
// Redis replay cache) and by either tier alone; the handler owns neither the
// storage contracts nor the snapshot wire format. A nil replayer disables the
// replay path entirely, so the live route works unchanged when replay is not
// wired.
//
// Only a finite video whose analysis ran to clean completion ever has a stored
// snapshot (the live recorder and the pre-analysis job persist on no other
// path), so a live stream never hits here and the "finite videos only"
// constraint is satisfied by the stores' contents rather than a separate
// video-kind check.
type AnalysisReplayer interface {
	Snapshot(ctx context.Context, videoID string) (events []service.LiveEvent, found bool, err error)
}

// snapshotPersistTimeout bounds the post-session cache write. The write runs
// after the socket has closed, on a context detached from the (now-canceled)
// request, so it needs its own deadline: a slow or wedged cache must not pin the
// handler goroutine indefinitely. It is generous - the user's session has already
// ended, so this only protects against an unbounded hang, not latency the user
// feels.
const snapshotPersistTimeout = 10 * time.Second

// liveReadLimit bounds one inbound audio frame. At 16 kHz mono 16-bit PCM a
// second of audio is 32 KB, so 1 MiB leaves ample room for coarse client
// chunking while rejecting a runaway frame.
const liveReadLimit = 1 << 20

// liveWriteTimeout bounds a single outbound frame write so a stalled client
// cannot wedge the session indefinitely.
const liveWriteTimeout = 10 * time.Second

// livePingInterval and livePingTimeout drive a keepalive ping that detects a
// half-open connection (a peer that vanished without a close frame: laptop
// sleep, network drop, crashed tab). Without it, conn.Read on a dead peer
// blocks forever, pinning the reader, the writer, and the upstream provider
// session. The ping tolerates legitimate playback pauses: the browser answers
// pings even when it is sending no audio, so only a genuinely dead peer trips it.
const (
	livePingInterval = 30 * time.Second
	livePingTimeout  = 10 * time.Second
)

// liveAudioBuffer bounds how many inbound frames may queue between the socket
// reader and the analyzer. A small buffer absorbs transient analysis stalls so
// the audio reader keeps returning to conn.Read (where the library services pong
// frames), which keeps the keepalive ping from mistaking backpressure for a
// dead peer. It also bounds memory; sustained overload still applies
// backpressure once the buffer fills. At ~100 ms/frame this is a few seconds.
const liveAudioBuffer = 32

// interimFrame is the wire form of an interim event: the live, still-revised
// caption for the current utterance. It carries only text - no id, no
// timestamps, no verdict - and the next interim or subtitle supersedes it.
type interimFrame struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// subtitleFrame is the wire form of a subtitle event: a statement's text the
// moment it is transcribed, before any verdict. Speaker carries the diarized
// speaker label when the live provider supplies one; it is additive and omitted
// when empty, so a client that does not yet read it is unaffected.
type subtitleFrame struct {
	Type    string  `json:"type"`
	ID      string  `json:"id"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Text    string  `json:"text"`
	Speaker string  `json:"speaker,omitempty"`
}

// consistencyFrame is the wire form of a consistency event: the same speaker
// contradicted an earlier statement. ID is the offending statement (the one the
// flag renders on); EarlierID and EarlierText identify the prior statement it
// conflicts with so the client can link back to it. Speaker and Rationale are
// additive context. It is delivered after the statement's subtitle and result.
type consistencyFrame struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	EarlierID   string `json:"earlier_id"`
	EarlierText string `json:"earlier_text"`
	Speaker     string `json:"speaker,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

// resultFrame is the wire form of a result event: the shared per-segment shape
// (embedded segmentJSON, the single home of the verdict wire shaping) plus the
// correlation id and an optional non-fatal analysis error.
type resultFrame struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	segmentJSON
	Error string `json:"error,omitempty"`
}

// atomicClaimJSON is one atomic claim on a claims frame: the stable id the
// client keys per-claim results on, its coreference-resolved text, and its
// initial pending status. Quote is the verbatim run of statement words the
// claim was extracted from and Spans locates those words inside the unit's
// member segments (by subtitle id and [start, end) rune offsets), so the client
// highlights the exact words that were checked. Both are additive and omitted
// when the decomposer could not anchor the claim, so an older client or an
// unanchored claim renders exactly as before.
type atomicClaimJSON struct {
	ClaimID string             `json:"claim_id"`
	Text    string             `json:"text"`
	Status  string             `json:"status"`
	Quote   string             `json:"quote,omitempty"`
	Spans   []domain.ClaimSpan `json:"spans,omitempty"`
}

// claimsFrame is the wire form of a claims event (retrieve-then-verify path): a
// unit's atomic claims, each pending a verdict. It shares the unit's correlation
// id so the client groups the claims under the statement they decomposed from.
// SegmentIDs lists the unit's member subtitle ids in order so the client can
// merge the whole group into one displayed statement; it is additive and
// omitted when empty, so an older client renders per-statement exactly as
// before.
type claimsFrame struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Claims     []atomicClaimJSON `json:"claims"`
	SegmentIDs []string          `json:"segment_ids,omitempty"`
}

// claimResultType is the wire discriminator for a per-claim result on the
// retrieve-then-verify path. It is deliberately distinct from the legacy
// "result" type so a client dispatches per-claim verdicts cleanly and a client
// that does not yet understand them drops the frame rather than mis-decoding it
// as a legacy segment result (whose start/end/text/matches it lacks).
const claimResultType = "claim_result"

// claimResultFrame is the wire form of a per-claim result on the
// retrieve-then-verify path: keyed on claim_id so the client replaces a claim's
// row in place as it goes checking -> verified (or unchecked). Status is the
// lifecycle state; Source tags a verified verdict's origin (curated|verified);
// Verdict, Confidence, Citations, and Rationale are present once verified. The
// embedded matches carry the cited evidence (with evidence_id) so the grounding
// round-trips to the UI; they are the operator detail payload, emitted only when
// DEBUG_FACT_CHECK is on (see toClaimResultFrame).
//
// SourceLabel is the French publisher label of the verdict's winning citation
// (Assemblée nationale, INSEE, Wikipédia, ...), distinct from Source (which tags
// the verdict's curated|verified origin). SourceURL is that citation's canonical
// link, so the chip links the source. Both are omitted for a knowledge-only or
// curated-borrow verdict that names no provider, so the chip is then absent
// rather than empty.
//
// Literal and Flags are the political path's two orthogonal axes (FACTCHECK_POLITICAL
// on): Literal is the face-value verdict (accurate|inaccurate|unverifiable) and
// Flags is the subset of the closed manipulation vocabulary that applies to the
// framing. Both are omitted on the credibility-only path, so a flag-off frame is
// byte-for-byte the legacy shape; the frontend (VER-104) keys its two-axis display
// on them.
type claimResultFrame struct {
	Type        string                `json:"type"`
	ID          string                `json:"id"`
	ClaimID     string                `json:"claim_id"`
	Status      string                `json:"status"`
	Source      string                `json:"source,omitempty"`
	SourceLabel string                `json:"source_label,omitempty"`
	SourceURL   string                `json:"source_url,omitempty"`
	Verdict     string                `json:"verdict,omitempty"`
	Basis       string                `json:"basis,omitempty"`
	Literal     string                `json:"literal,omitempty"`
	Flags       []string              `json:"flags,omitempty"`
	Confidence  *float64              `json:"confidence,omitempty"`
	Rationale   string                `json:"rationale,omitempty"`
	Matches     []domain.SegmentMatch `json:"matches,omitempty"`
	SkipReason  string                `json:"skip_reason,omitempty"`
	Error       string                `json:"error,omitempty"`
}

// speakerTallyFrame is the wire form of a speaker-tally event (retrieve-then-verify
// path): a speaker's running verdict counts, so the client can render a per-speaker
// itemized breakdown keyed on speaker. It is additive - a client that does not
// understand it drops the frame and renders everything else unchanged.
//
// MisleadingFraming is the political path's separate count of the speaker's claims
// that carried at least one manipulation flag, orthogonal to the credibility
// tallies, so the UI can distinguish an outright falsehood from honest-but-
// misleading framing. It is omitted when zero, so the credibility-only path (which
// never flags a claim) keeps its byte-for-byte wire shape; the frontend treats an
// absent value as zero.
type speakerTallyFrame struct {
	Type              string `json:"type"`
	Speaker           string `json:"speaker"`
	Credible          int    `json:"credible"`
	Disputed          int    `json:"disputed"`
	Unverifiable      int    `json:"unverifiable"`
	MisleadingFraming int    `json:"misleading_framing,omitempty"`
}

// liveHandler upgrades the request to a WebSocket and bridges it to the live
// analyzer: a reader pumps inbound audio frames to the analyzer while the main
// goroutine writes the analyzer's events back. allowedOrigins are the browser
// origins permitted to connect cross-origin; empty enforces same-origin.
// debugFactCheckEnabled is the server-side enable for the per-claim evidence
// detail payload (the cited matches with their passages and scores). The detail
// is emitted only when it is on AND the caller carries a verified admin claim,
// so it is never exposed in production (where the flag is off) and never to a
// non-admin caller (whose role is read from the request context, not any
// client-supplied flag); otherwise only the source label is sent.
//
// The admin claim is read from the request context, where the /api identity gate
// (middleware.RequireIdentity) placed the verified identity. The browser
// WebSocket API cannot set the Authorization header, so the gate also accepts the
// Keycloak token on the access_token query parameter; a browser admin carries it
// there and sees the detail, while a guest does not. The gate is the authoritative
// server-side admin check: nothing a client sends can flip the detail on.
func liveHandler(analyzer LiveAnalyzer, recorder AnalysisRecorder, replayer AnalysisReplayer, allowedOrigins []string, debugFactCheckEnabled bool, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		debugFactCheck := debugFactCheckEnabled && middleware.IdentityFrom(r.Context()).IsAdmin()
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
		if err != nil {
			// Accept has already written the handshake failure response.
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(liveReadLimit)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		videoID := r.PathValue("id")

		// Cache-hit fast path: a finite video that previously completed has its whole
		// analysis cached, so it is replayed instantly here - the full transcript and
		// every verdict, in capture order, through the same serializer the live path
		// uses - without ever constructing the transcriber or analyzer. Only a clean
		// completion is ever cached, so a live stream never has a snapshot and falls
		// through. A miss, a disabled cache, a version mismatch, or a backend error all
		// report found=false, so the live pipeline below is the single, unchanged
		// fallback. The replayed frames carry the same playback timestamps as the live
		// stream, so the client's active-subtitle highlight keeps working with the whole
		// session loaded up front.
		if replayer != nil {
			events, found, err := replayer.Snapshot(ctx, videoID)
			if err != nil {
				logger.ErrorContext(ctx, "analysis cache lookup failed", slog.String("video_id", videoID), slog.Any("err", err))
			} else if found {
				replayEvents(ctx, conn, events, debugFactCheck, logger, videoID)
				_ = conn.Close(websocket.StatusNormalClosure, "")
				return
			}
		}

		audio := make(chan []byte, liveAudioBuffer)
		reader := newAudioReader()
		go reader.run(ctx, cancel, conn, audio)
		go pingLoop(ctx, cancel, conn, livePingInterval, livePingTimeout)

		events, err := analyzer.Run(ctx, audio)
		if err != nil {
			logger.ErrorContext(ctx, "live analyze start failed", slog.String("video_id", videoID), slog.Any("err", err))
			_ = conn.Close(websocket.StatusInternalError, "analysis unavailable")
			return
		}

		captured, drained := writeEvents(ctx, cancel, conn, events, debugFactCheck, logger, videoID)
		// Capture the request's abort state before closing the socket: conn.Close
		// tears down the underlying connection, which can cancel r.Context(), so
		// reading it afterwards would misread a clean completion as an aborted one.
		requestAborted := r.Context().Err() != nil
		_ = conn.Close(websocket.StatusNormalClosure, "")

		// Persist a snapshot only on a genuine completion, telling it apart from an
		// early disconnect by three independent signals that must all hold:
		//   - the client closed the socket cleanly (StatusNormalClosure), i.e. it
		//     finished streaming the finite video's audio to EOF rather than dropping;
		//   - the event stream drained to its close (the pipeline flushed every unit),
		//     not a mid-stream write failure (which forces a cancel and so is false);
		//   - the request was not otherwise aborted (a server shutdown cancels r.Context()).
		// A live stream never closes cleanly at an EOF it does not have, and a viewer
		// who navigates away aborts the socket, so both fail the clean-close check and
		// persist nothing. Any signal failing discards the accumulator unwritten. The
		// reader.cleanClose read is ordered after the reader goroutine returns (it is
		// what closes audio and so ends the analyzer and this writer loop); the wait
		// below also covers the write-failure path where the loop exits first.
		<-reader.done
		if reader.cleanClose && drained && !requestAborted {
			persistSnapshot(recorder, videoID, captured, logger)
		}
	}
}

// audioReader forwards inbound audio and records whether the client ended the
// stream with a clean close - the finite video's audio reaching EOF - which is
// the handler's completion signal. done is closed when the reader returns so the
// handler can read cleanClose with a happens-before guarantee on every exit path,
// including a writer-side failure that unwinds the reader concurrently.
type audioReader struct {
	cleanClose bool
	done       chan struct{}
}

func newAudioReader() *audioReader {
	return &audioReader{done: make(chan struct{})}
}

// persistSnapshot writes the completed analysis to the cache after the live
// session has ended. It is best-effort and never affects the user, who has
// already disconnected: it runs on a fresh background context so the write is not
// aborted by the very disconnect that completed the stream, under its own timeout
// so a slow cache cannot pin the goroutine, and any error is logged rather than
// surfaced. A nil recorder makes capture a no-op.
func persistSnapshot(recorder AnalysisRecorder, videoID string, events []service.LiveEvent, logger *slog.Logger) {
	if recorder == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotPersistTimeout)
	defer cancel()
	if err := recorder.Persist(ctx, videoID, events); err != nil {
		logger.ErrorContext(ctx, "persisting analysis snapshot failed", slog.String("video_id", videoID), slog.Any("err", err))
	}
}

// run forwards inbound binary frames to the analyzer until the client closes, a
// read fails, or ctx is canceled. Non-binary frames are ignored. It closes audio
// on exit so the analyzer's stream ends, cancels the session so the writer stops
// too, and closes done so the handler can read cleanClose. A read that returns a
// normal-closure close error is the finite video's audio reaching EOF: the client
// streamed every frame and closed politely, the one exit that marks a completion.
// An abnormal close, any other read error, or a context cancel leaves cleanClose
// false, so a dropped viewer or a live stream never reads as complete.
func (a *audioReader) run(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, audio chan<- []byte) {
	defer close(a.done)
	defer close(audio)
	defer cancel()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			a.cleanClose = ctx.Err() == nil && websocket.CloseStatus(err) == websocket.StatusNormalClosure
			return
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case audio <- data:
		}
	}
}

// pingLoop pings the peer on a fixed interval and cancels the session when a
// ping is not answered within timeout, so a half-open connection is reclaimed
// instead of blocking the reader forever. It exits when ctx is canceled.
func pingLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, timeout)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// writeEvents serializes each live event to a JSON text frame until the event
// stream ends or a write fails. A write failure cancels the session so the
// reader unwinds and no goroutine leaks. Every write is bounded by
// liveWriteTimeout, so a failure - including an interim that the client could not
// accept within the deadline - means the connection is broken or wedged and the
// session is torn down at once rather than burning a per-frame timeout on it.
//
// It tees the stream as it goes: every event it forwards to the client is also
// appended to the returned slice, unchanged, so a finite video's completed
// analysis can be persisted as a snapshot without altering a single wire frame.
// drained reports whether the loop ended because the event channel closed (the
// pipeline finished) rather than because a write failed - the caller persists a
// snapshot only when drained is true, never on a truncated session.
func writeEvents(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, events <-chan service.LiveEvent, debugFactCheck bool, logger *slog.Logger, videoID string) (captured []service.LiveEvent, drained bool) {
	for ev := range events {
		captured = append(captured, ev)
		if err := writeEvent(ctx, conn, ev, debugFactCheck); err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "live event write failed", slog.String("video_id", videoID), slog.Any("err", err))
			}
			cancel()
			return captured, false
		}
	}
	return captured, true
}

// replayEvents re-emits a cached snapshot's events to the client in capture
// order, through the very same per-kind serializer the live path uses, so a
// cache-served session is byte-for-byte the live one at the wire. It is the read
// counterpart of writeEvents' tee: that path captured these events unchanged, and
// this path replays them unchanged. It stops on the first write failure (a broken
// or wedged client) - there is nothing to drain or cancel, the session has no
// upstream pipeline - and logs the failure unless ctx is already done. debugFactCheck
// gates the per-claim evidence detail exactly as it does live, so a cached frame
// is shaped identically to its live original for the same caller.
func replayEvents(ctx context.Context, conn *websocket.Conn, events []service.LiveEvent, debugFactCheck bool, logger *slog.Logger, videoID string) {
	for _, ev := range events {
		if err := writeEvent(ctx, conn, ev, debugFactCheck); err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "cached event replay write failed", slog.String("video_id", videoID), slog.Any("err", err))
			}
			return
		}
	}
}

// writeEvent writes one event under a bounded deadline, shaping it by kind. A
// malformed event that shapes to no frame is skipped without a write.
func writeEvent(ctx context.Context, conn *websocket.Conn, ev service.LiveEvent, debugFactCheck bool) error {
	frame := toLiveFrame(ev, debugFactCheck)
	if frame == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, liveWriteTimeout)
	defer cancel()
	return wsjson.Write(ctx, conn, frame)
}

// toLiveFrame shapes one live event into its wire frame, the single home of
// the event-to-frame mapping: the live socket writes each frame as its own
// JSON message and the stored-analysis read API returns the same frames as an
// array, so a REST-hydrated session is byte-for-byte the live one at the
// frame level. It returns nil for a malformed event (a tally or consistency
// event missing its payload), which every caller skips. The heterogeneous
// frame structs meet here at the JSON boundary, hence the any return.
func toLiveFrame(ev service.LiveEvent, debugFactCheck bool) any {
	if ev.Kind == service.LiveEventInterim {
		return interimFrame{
			Type: string(ev.Kind),
			Text: ev.Segment.Text,
		}
	}
	if ev.Kind == service.LiveEventSubtitle {
		return subtitleFrame{
			Type:    string(ev.Kind),
			ID:      ev.ID,
			Start:   ev.Segment.Start.Seconds(),
			End:     ev.Segment.End.Seconds(),
			Text:    ev.Segment.Text,
			Speaker: ev.Segment.Speaker,
		}
	}
	if ev.Kind == service.LiveEventSpeakerTally {
		// The service always sets SpeakerTally for this kind; the guard keeps a
		// malformed event from emitting a zero-valued tally frame.
		if ev.SpeakerTally == nil {
			return nil
		}
		return speakerTallyFrame{
			Type:              string(ev.Kind),
			Speaker:           ev.SpeakerTally.Speaker,
			Credible:          ev.SpeakerTally.Credible,
			Disputed:          ev.SpeakerTally.Disputed,
			Unverifiable:      ev.SpeakerTally.Unverifiable,
			MisleadingFraming: ev.SpeakerTally.MisleadingFraming,
		}
	}
	if ev.Kind == service.LiveEventClaims {
		claims := make([]atomicClaimJSON, len(ev.Claims))
		for i, c := range ev.Claims {
			claims[i] = atomicClaimJSON{ClaimID: c.ClaimID, Text: c.Text, Status: string(service.ClaimStatusPending), Quote: c.Quote, Spans: c.Spans}
		}
		return claimsFrame{
			Type:       string(ev.Kind),
			ID:         ev.ID,
			Claims:     claims,
			SegmentIDs: ev.SegmentIDs,
		}
	}
	// A result event carrying a claim id is a per-claim verdict on the
	// retrieve-then-verify path; it shapes differently from the legacy per-segment
	// result, keyed on claim_id so the client replaces the claim's row in place.
	if ev.Kind == service.LiveEventResult && ev.ClaimID != "" {
		return toClaimResultFrame(ev, debugFactCheck)
	}
	if ev.Kind == service.LiveEventConsistency {
		// The service always sets Consistency for this kind; the guard keeps a
		// future or malformed event from panicking the writer (and from falling
		// through to a bogus result frame) - it is simply skipped.
		if ev.Consistency == nil {
			return nil
		}
		return consistencyFrame{
			Type:        string(ev.Kind),
			ID:          ev.ID,
			EarlierID:   ev.Consistency.EarlierID,
			EarlierText: ev.Consistency.EarlierText,
			Speaker:     ev.Consistency.Speaker,
			Rationale:   ev.Consistency.Rationale,
		}
	}
	return resultFrame{
		Type:        string(ev.Kind),
		ID:          ev.ID,
		segmentJSON: toSegmentJSON(domain.SegmentResult{Segment: ev.Segment, Matches: ev.Matches, SkipReason: ev.SkipReason, Confidence: ev.Confidence}),
		Error:       ev.Err,
	}
}

// toClaimResultFrame shapes one per-claim result event into its wire frame. The
// verdict fields are present only once verified; a checking or unchecked result
// carries just its status (and, for unchecked, the capacity skip reason). The
// winning citation's source label and link are always surfaced so a normal
// viewer sees the clean provenance; the full cited evidence (the detail payload)
// rides along only when debugFactCheck is on, so the per-passage detail is never
// emitted in production.
func toClaimResultFrame(ev service.LiveEvent, debugFactCheck bool) claimResultFrame {
	f := claimResultFrame{
		Type:       claimResultType,
		ID:         ev.ID,
		ClaimID:    ev.ClaimID,
		Status:     string(ev.ClaimStatus),
		Source:     string(ev.Source),
		SkipReason: string(ev.SkipReason),
		Error:      ev.Err,
	}
	if ev.Verdict != nil {
		f.Verdict = ev.Verdict.Verdict
		f.Basis = ev.Verdict.Basis
		confidence := ev.Verdict.Confidence
		f.Confidence = &confidence
		f.Rationale = ev.Verdict.Rationale
		provenance := domain.WinningSource(ev.Verdict.Citations)
		f.SourceLabel = provenance.Label
		f.SourceURL = provenance.URL
		if debugFactCheck {
			f.Matches = ev.Verdict.Citations
		}
		// The two-axis fields are populated only on the political path; on the
		// credibility-only path they are zero and omitted, keeping the wire shape
		// unchanged.
		f.Literal = ev.Verdict.Literal
		f.Flags = ev.Verdict.Flags
	}
	return f
}
