"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { usePlayback, usePlaybackStore } from "@/components/playback/playback-provider";
import { createMediaElementCapture } from "@/lib/live/audio-capture";
import {
  parseLiveFrame,
  type ResultFrame,
  type SubtitleFrame,
} from "@/lib/live/frames";
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
};

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
function prepareFrame(
  frame: SubtitleFrame | ResultFrame,
  sessionSeq: number,
  baseTime: number,
): SubtitleFrame | ResultFrame {
  const id = `${sessionSeq}:${frame.id}`;
  if (frame.type === "subtitle") {
    return {
      ...frame,
      id,
      start: frame.start + baseTime,
      end: frame.end + baseTime,
    };
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

  // Latest factories/values for the long-lived dispatch closure, refreshed after
  // each render so the controller (created once) always calls the current ones.
  const socketFactoryRef = useRef(socketFactory);
  const captureFactoryRef = useRef(captureFactory);
  const videoIdRef = useRef(videoId);
  useEffect(() => {
    socketFactoryRef.current = socketFactory;
    captureFactoryRef.current = captureFactory;
    videoIdRef.current = videoId;
  });

  const dispatchRef = useRef<(event: LiveSessionEvent) => void>(() => {});

  useEffect(() => {
    function ingestFrame(raw: string, sessionSeq: number): void {
      const frame = parseLiveFrame(raw);
      if (!frame) {
        return;
      }
      if (frame.type === "interim") {
        setCaption(frame.text);
        return;
      }
      // A subtitle commits the current utterance to a statement, so the live
      // caption clears - its text now lives in the statement list.
      if (frame.type === "subtitle") {
        setCaption("");
      }
      const prepared = prepareFrame(frame, sessionSeq, baseTimeRef.current);
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

  return { statements: orderedStatements, caption, status };
}
