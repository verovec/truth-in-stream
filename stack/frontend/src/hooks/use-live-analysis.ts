"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { usePlayback, usePlaybackStore } from "@/components/playback/playback-provider";
import { createMediaElementCapture } from "@/lib/live/audio-capture";
import {
  type ClaimResultFrame,
  type ClaimsFrame,
  type ConsistencyFrame,
  parseLiveFrame,
  type ResultFrame,
  type SubtitleFrame,
} from "@/lib/live/frames";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  type ClaimsState,
  claimsForUnit,
  dropUnits,
  emptyClaims,
  type LiveClaim,
} from "@/lib/live/claims";
import { createLiveSocket } from "@/lib/live/socket";
import type { AudioCaptureFactory, LiveSocketFactory } from "@/lib/live/ports";
import {
  initialSession,
  type LiveSessionCommand,
  type LiveSessionEvent,
  type LiveSessionState,
  type LiveStatus,
  liveStatus,
  MAX_RECONNECT_ATTEMPTS,
  reduceSession,
} from "@/lib/live/session";
import {
  applyFrame,
  clearAnalysing,
  emptyStatements,
  type LiveStatement,
  listStatements,
  type StatementsState,
} from "@/lib/live/statements";
import { type LiveSummary, summarizeStatements } from "@/lib/live/summary";
import { liveSocketUrl } from "@/lib/live/url";
import type { LiveSocket } from "@/lib/live/ports";

export type UseLiveAnalysisOptions = {
  socketFactory?: LiveSocketFactory;
  captureFactory?: AudioCaptureFactory;
};

export type LiveAnalysis = {
  statements: LiveStatement[];
  caption: string;
  status: LiveStatus;
  summary: LiveSummary;
  // claimsFor returns one statement's atomic claims in announced order, empty on
  // a legacy stream that emits no claim frames. The subtitle list reads it per
  // row to render the progressive per-claim disclosure under each statement.
  claimsFor: (statementId: string) => LiveClaim[];
};

// survivingUnitIds returns the ids of statements that clearAnalysing keeps (the
// checked ones), so the claims store can be pruned to exactly the units that
// remain after an in-flight reset, dropping the claims of dangling statements.
function survivingUnitIds(state: StatementsState): Set<string> {
  const ids = new Set<string>();
  for (const [id, statement] of state.byId) {
    if (statement.status === "checked") {
      ids.add(id);
    }
  }
  return ids;
}

// reconnectDelayMs grows the backoff with each consecutive failed attempt,
// capped so a persistently-down backend retries on a steady cadence rather than
// hammering it. attempt is 1-based.
function reconnectDelayMs(attempt: number): number {
  return Math.min(1000 * 2 ** (attempt - 1), 8000);
}

// prepareFrame shifts a frame's stream-relative timestamps onto the playback
// clock (baseTime is the video position when the session opened) and namespaces
// its correlation id by session, so ids from a reconnect cannot collide with a
// prior session's.
type CommittedFrame =
  | SubtitleFrame
  | ResultFrame
  | ConsistencyFrame
  | ClaimsFrame
  | ClaimResultFrame;

function prepareFrame(
  frame: CommittedFrame,
  sessionSeq: number,
  baseTime: number,
): CommittedFrame {
  const id = `${sessionSeq}:${frame.id}`;
  if (frame.type === "subtitle") {
    return {
      ...frame,
      id,
      start: frame.start + baseTime,
      end: frame.end + baseTime,
    };
  }
  if (frame.type === "consistency") {
    // A consistency frame carries no timestamps; both ids it references are
    // namespaced so it resolves to this session's statements after a reconnect.
    return { ...frame, id, earlierId: `${sessionSeq}:${frame.earlierId}` };
  }
  if (frame.type === "claims" || frame.type === "claim_result") {
    // Claim frames carry the unit's correlation id and no timestamps; namespacing
    // the unit id alone keys them to this session's statement after a reconnect.
    // claim_id is per-unit and needs no session prefix - it is only ever read
    // within a unit already namespaced by id.
    return { ...frame, id };
  }
  return {
    ...frame,
    id,
    segment: {
      ...frame.segment,
      start: frame.segment.start + baseTime,
      end: frame.segment.end + baseTime,
    },
  };
}

/**
 * Drives live fact-check analysis for one video: it opens a WebSocket when
 * playback starts, captures the playing audio as 16 kHz PCM and streams it
 * paced to playback, and folds the incremental subtitle/result frames into a
 * timestamp-ordered statement list. Pausing suspends capture, resuming
 * continues it, and seeking resets the session cleanly while preserving
 * resolved verdicts. A dropped connection reconnects with backoff up to a cap.
 *
 * The capture/stream control flow lives in the pure session reducer; this hook
 * only interprets its commands against the real (or injected) socket and audio
 * ports.
 */
export function useLiveAnalysis(
  videoId: string,
  options: UseLiveAnalysisOptions = {},
): LiveAnalysis {
  const store = usePlaybackStore();
  const paused = usePlayback((snapshot) => snapshot.paused);

  const socketFactory = options.socketFactory ?? createLiveSocket;
  const captureFactory = options.captureFactory ?? createMediaElementCapture;

  const [statements, setStatements] = useState<StatementsState>(emptyStatements);
  // claims holds the per-claim verify-path lifecycle, keyed by unit id then
  // claim_id. It is empty on a legacy stream that emits no claim frames, so the
  // statement list renders exactly as before when no claims arrive.
  const [claims, setClaims] = useState<ClaimsState>(emptyClaims);
  // caption is the live, still-being-spoken utterance from interim frames,
  // shown verbatim until it commits to a statement or the session resets.
  const [caption, setCaption] = useState("");
  const [status, setStatus] = useState<LiveStatus>("idle");

  // All session machinery lives in refs so socket/audio callbacks and effects
  // mutate one controller instance instead of racing React state.
  const sessionRef = useRef<LiveSessionState>(initialSession());
  const socketRef = useRef<LiveSocket | null>(null);
  const captureRef = useRef<ReturnType<AudioCaptureFactory> | null>(null);
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const sessionSeqRef = useRef(0);
  const baseTimeRef = useRef(0);
  // Tracks whether the stream is actively live, read by the frame callback to
  // gate interim captions: pause/seek leave the socket open, so a trailing
  // interim must not repopulate the caption once the stream has left "live".
  const liveRef = useRef(false);

  // Latest factories/values for the long-lived dispatch closure, refreshed after
  // each render so the controller (created once) always calls the current ones.
  const socketFactoryRef = useRef(socketFactory);
  const captureFactoryRef = useRef(captureFactory);
  const videoIdRef = useRef(videoId);
  // statementsRef mirrors the latest statements so the long-lived command closure
  // can read which units survive a clearAnalysing without closing over a stale
  // state value; the clear runs synchronously off this same snapshot.
  const statementsRef = useRef(statements);
  useEffect(() => {
    socketFactoryRef.current = socketFactory;
    captureFactoryRef.current = captureFactory;
    videoIdRef.current = videoId;
    liveRef.current = status === "live";
    statementsRef.current = statements;
  });

  const dispatchRef = useRef<(event: LiveSessionEvent) => void>(() => {});

  useEffect(() => {
    function ingestFrame(raw: string, sessionSeq: number): void {
      const frame = parseLiveFrame(raw);
      if (!frame) {
        return;
      }
      if (frame.type === "interim") {
        // Only while live: pause/seek leave the socket open, so a late interim
        // would otherwise re-show the caption the status change just cleared.
        if (liveRef.current) {
          setCaption(frame.text);
        }
        return;
      }
      // A subtitle commits the current utterance to a statement, so the live
      // caption clears - its text now lives in the statement list.
      if (frame.type === "subtitle") {
        setCaption("");
      }
      const prepared = prepareFrame(frame, sessionSeq, baseTimeRef.current);
      if (prepared.type === "claims") {
        setClaims((prev) => applyClaimsFrame(prev, prepared));
        return;
      }
      if (prepared.type === "claim_result") {
        setClaims((prev) => applyClaimResultFrame(prev, prepared));
        return;
      }
      setStatements((prev) => applyFrame(prev, prepared));
    }

    function ensureCapture(): void {
      if (captureRef.current) {
        return;
      }
      const element = store.getMediaElement();
      if (!element) {
        return;
      }
      captureRef.current = captureFactoryRef.current(element, (frame) => {
        socketRef.current?.send(frame);
      });
    }

    function openSocket(): void {
      sessionSeqRef.current += 1;
      const seq = sessionSeqRef.current;
      baseTimeRef.current = store.getSnapshot().currentTime;
      const url = liveSocketUrl(videoIdRef.current);
      socketRef.current = socketFactoryRef.current(url, {
        onOpen: () => {
          if (seq === sessionSeqRef.current) {
            dispatchRef.current({ type: "socketOpen" });
          }
        },
        onFrame: (raw) => {
          if (seq === sessionSeqRef.current) {
            ingestFrame(raw, seq);
          }
        },
        onClose: (clean) => {
          if (seq === sessionSeqRef.current) {
            dispatchRef.current({ type: "socketClose", clean });
          }
        },
      });
    }

    function closeSocket(): void {
      // Invalidate the session so the socket's own onClose, fired by this
      // intentional close, is ignored and does not re-drive the machine.
      sessionSeqRef.current += 1;
      socketRef.current?.close();
      socketRef.current = null;
    }

    function runCommand(command: LiveSessionCommand): void {
      switch (command) {
        case "openSocket":
          openSocket();
          break;
        case "closeSocket":
          closeSocket();
          break;
        case "startCapture":
        case "resumeCapture":
          ensureCapture();
          captureRef.current?.resume();
          break;
        case "suspendCapture":
          captureRef.current?.suspend();
          break;
        case "stopCapture":
          captureRef.current?.stop();
          captureRef.current = null;
          break;
        case "clearAnalysing":
          setStatements((prev) => clearAnalysing(prev));
          // Drop the claims of any unit whose statement is in-flight (dropped by
          // clearAnalysing), keeping the claims of statements that survive so a
          // resolved unit's verdicts are not lost on reconnect. The updater reads
          // the surviving ids from the same cleared set, so the two stay in sync.
          setClaims((prev) =>
            dropUnits(prev, survivingUnitIds(statementsRef.current)),
          );
          // The caption is not cleared here: a reset (seek or a dropped
          // connection) always moves status off "live", and the status effect
          // below clears it. Keeping that the single owner avoids two clear sites.
          break;
        case "scheduleReconnect":
          if (reconnectTimerRef.current) {
            clearTimeout(reconnectTimerRef.current);
          }
          reconnectTimerRef.current = setTimeout(
            () => dispatchRef.current({ type: "reconnect" }),
            reconnectDelayMs(
              Math.min(sessionRef.current.attempts, MAX_RECONNECT_ATTEMPTS),
            ),
          );
          break;
        case "cancelReconnect":
          if (reconnectTimerRef.current) {
            clearTimeout(reconnectTimerRef.current);
            reconnectTimerRef.current = null;
          }
          break;
      }
    }

    function dispatch(event: LiveSessionEvent): void {
      const { state, commands } = reduceSession(sessionRef.current, event);
      sessionRef.current = state;
      const nextStatus = liveStatus(state);
      setStatus(nextStatus);
      // The live caption is only meaningful while streaming. Clear it on any
      // transition out of live (pause, reconnect, error, end) here in the event
      // path - status only ever changes through dispatch - so a partial from the
      // last live moment never lingers or flashes on resume, without clearing
      // state from inside an effect.
      if (nextStatus !== "live") {
        setCaption("");
      }
      for (const command of commands) {
        runCommand(command);
      }
    }

    dispatchRef.current = dispatch;
    const unsubscribeSeek = store.subscribeSeeked(() =>
      dispatch({ type: "seek" }),
    );

    return () => {
      unsubscribeSeek();
      dispatch({ type: "stop" });
    };
  }, [store, videoId]);

  // Translate playback play/pause into session events; effect keyed on paused so
  // it fires only on a real transition, not on every timeupdate.
  useEffect(() => {
    dispatchRef.current({ type: paused ? "pause" : "play" });
  }, [paused]);

  // Memoize the ordered list so a caption-only update (every interim word) does
  // not produce a new array reference and re-render the memoized statement list.
  const orderedStatements = useMemo(() => listStatements(statements), [statements]);

  // The running summary is a pure projection of the same statements, memoized on
  // them so an interim caption (which never touches the statement set) does not
  // recompute it. This is the single source the top-of-page strip and the
  // fact-check list both read, so they can never disagree.
  const summary = useMemo(
    () => summarizeStatements(orderedStatements),
    [orderedStatements],
  );

  // claimsFor is memoized on the claims state so its identity is stable across
  // interim-only and statement-only updates (which never touch claims), keeping
  // the memoized statement list from re-rendering when only a caption changed.
  const claimsFor = useMemo(
    () => (statementId: string) => claimsForUnit(claims, statementId),
    [claims],
  );

  return { statements: orderedStatements, caption, status, summary, claimsFor };
}
