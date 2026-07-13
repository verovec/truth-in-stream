"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { parseLiveFrame } from "@/lib/live/frames";
import {
  applySpeakerTallyFrame,
  emptySpeakers,
  listSpeakers,
  type SpeakersState,
} from "@/lib/live/speakers";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  type ClaimsState,
  claimsForUnit,
  emptyClaims,
} from "@/lib/live/claims";
import { createLiveSocket } from "@/lib/live/socket";
import type { LiveSocket, LiveSocketFactory } from "@/lib/live/ports";
import { type LiveStatus, MAX_RECONNECT_ATTEMPTS } from "@/lib/live/session";
import {
  applyFrame,
  emptyStatements,
  listStatements,
  type StatementsState,
} from "@/lib/live/statements";
import { summarizeStatements } from "@/lib/live/summary";
import { channelLiveSocketUrl } from "@/lib/live/url";
import type { LiveAnalysis } from "@/hooks/use-live-analysis";

// Re-exported as the hook's return type, so consumers (and tests) import it
// alongside useChannelLive.
export type { LiveAnalysis };

export type UseChannelLiveOptions = {
  socketFactory?: LiveSocketFactory;
};

// reconnectDelayMs grows the backoff with each consecutive failed attempt,
// capped so a persistently-down channel retries on a steady cadence. attempt is
// 1-based. It mirrors the video live hook's schedule.
function reconnectDelayMs(attempt: number): number {
  return Math.min(1000 * 2 ** (attempt - 1), 8000);
}

// offAirType reports whether a raw frame is the backend's channel-ended sentinel.
// parseLiveFrame returns null for an unknown type and so silently drops
// {"type":"off_air"}, so it is detected here before parsing: the viewer stream
// sends the backlog, then live frames, then off_air and closes.
function offAirType(raw: string): boolean {
  try {
    const value = JSON.parse(raw) as { type?: unknown };
    return value.type === "off_air";
  } catch {
    return false;
  }
}

/**
 * Drives read-only live fact-check analysis for one TV channel. Unlike the video
 * hook there is no audio to capture and no playback clock: this viewer opens a
 * WebSocket to the channel's viewer stream, ingests the recent backlog the
 * backend replays on connect, then folds live subtitle/claim/speaker frames into
 * the same statement/claim/speaker model the video path uses, so the shared live
 * components render it unchanged. It reconnects with backoff on an unclean close
 * and stops on an off_air sentinel or a clean close (the stream ended). Frames
 * are passed straight through with no timestamp offsetting.
 *
 * socketFactory is an injection seam; tests supply a fake transport.
 */
export function useChannelLive(
  channelId: string,
  options: UseChannelLiveOptions = {},
): LiveAnalysis {
  const socketFactory = options.socketFactory ?? createLiveSocket;

  const [statements, setStatements] = useState<StatementsState>(emptyStatements);
  const [claims, setClaims] = useState<ClaimsState>(emptyClaims);
  const [speakers, setSpeakers] = useState<SpeakersState>(emptySpeakers);
  const [caption, setCaption] = useState("");
  const [status, setStatus] = useState<LiveStatus>("connecting");

  // The factory is read from a ref refreshed each render so an inline factory
  // never re-runs the connection effect, which is keyed on channelId alone.
  const socketFactoryRef = useRef(socketFactory);
  useEffect(() => {
    socketFactoryRef.current = socketFactory;
  });

  // The connection effect is keyed on channelId, and ChannelLiveProvider keys
  // the driver on channelId too, so switching channels remounts this hook with
  // fresh state - a prior channel's transcript never bleeds into the next.
  useEffect(() => {
    let disposed = false;
    let offAir = false;
    let attempts = 0;
    let socket: LiveSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    function ingestFrame(raw: string): void {
      if (offAirType(raw)) {
        offAir = true;
        setCaption("");
        setStatus("ended");
        return;
      }
      const frame = parseLiveFrame(raw);
      if (!frame) {
        return;
      }
      if (frame.type === "interim") {
        setCaption(frame.text);
        return;
      }
      if (frame.type === "speaker_tally") {
        setSpeakers((prev) => applySpeakerTallyFrame(prev, frame));
        return;
      }
      if (frame.type === "subtitle") {
        // The utterance committed to a statement, so the live caption clears -
        // its text now lives in the statement list.
        setCaption("");
      }
      if (frame.type === "claims") {
        setClaims((prev) => applyClaimsFrame(prev, frame));
        return;
      }
      if (frame.type === "claim_result") {
        setClaims((prev) => applyClaimResultFrame(prev, frame));
        return;
      }
      setStatements((prev) => applyFrame(prev, frame));
    }

    function open(): void {
      setStatus(attempts === 0 ? "connecting" : "reconnecting");
      socket = socketFactoryRef.current(channelLiveSocketUrl(channelId), {
        onOpen: () => {
          if (disposed) {
            return;
          }
          attempts = 0;
          setStatus("live");
        },
        onFrame: (raw) => {
          if (!disposed) {
            ingestFrame(raw);
          }
        },
        onClose: (clean) => {
          if (disposed) {
            return;
          }
          socket = null;
          // A clean close or an off_air sentinel means the stream ended; do not
          // reconnect. An abrupt drop reconnects under the cap, then gives up.
          if (clean || offAir) {
            setStatus("ended");
            return;
          }
          attempts += 1;
          if (attempts > MAX_RECONNECT_ATTEMPTS) {
            setStatus("error");
            return;
          }
          setStatus("reconnecting");
          reconnectTimer = setTimeout(open, reconnectDelayMs(attempts));
        },
      });
    }

    open();

    return () => {
      disposed = true;
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
      socket?.close();
    };
  }, [channelId]);

  const orderedStatements = useMemo(
    () => listStatements(statements),
    [statements],
  );

  const summary = useMemo(
    () => summarizeStatements(orderedStatements, claims),
    [orderedStatements, claims],
  );

  const claimsFor = useMemo(
    () => (statementId: string) => claimsForUnit(claims, statementId),
    [claims],
  );

  const speakerList = useMemo(() => listSpeakers(speakers), [speakers]);

  return {
    statements: orderedStatements,
    caption,
    status,
    summary,
    claimsFor,
    speakers: speakerList,
  };
}
