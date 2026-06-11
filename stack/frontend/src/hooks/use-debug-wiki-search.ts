"use client";

// Drives the developer wiki-search bar (dev only). It opens one WebSocket to the
// backend probe, debounces keystrokes into query frames, and folds the replies
// in with the stale-response rule so a slow earlier query never overwrites a
// newer one. The socket factory and url are injectable so the orchestration is
// tested without a real network.
import { useCallback, useEffect, useRef, useState } from "react";

import {
  type DebugHit,
  encodeQueryFrame,
  parseDebugResultsFrame,
} from "@/lib/debug/frames";
import {
  applyResults,
  initialDebugSearchView,
  type DebugSearchView,
} from "@/lib/debug/search-state";
import type { LiveSocket, LiveSocketFactory } from "@/lib/live/ports";
import { createLiveSocket } from "@/lib/live/socket";
import { debugSearchUrl } from "@/lib/live/url";

// defaultDebounceMs waits out a burst of typing before issuing a query, so each
// embedding call corresponds to a settled query rather than a single keystroke.
const defaultDebounceMs = 250;

export type UseDebugWikiSearchOptions = {
  socketFactory?: LiveSocketFactory;
  url?: string;
  debounceMs?: number;
};

export type DebugWikiSearch = {
  query: string;
  hits: DebugHit[];
  error: string | null;
  connected: boolean;
  setQuery: (query: string) => void;
};

export function useDebugWikiSearch(
  options: UseDebugWikiSearchOptions = {},
): DebugWikiSearch {
  const { socketFactory, url, debounceMs = defaultDebounceMs } = options;

  const [query, setQueryState] = useState("");
  const [view, setView] = useState<DebugSearchView>(initialDebugSearchView);
  const [connected, setConnected] = useState(false);

  const socketRef = useRef<LiveSocket | null>(null);
  const seqRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const factory = socketFactory ?? createLiveSocket;
    const target = url ?? debugSearchUrl();
    const socket = factory(target, {
      onOpen: () => setConnected(true),
      onFrame: (raw) => {
        const frame = parseDebugResultsFrame(raw);
        if (frame) {
          setView((current) => applyResults(current, frame));
        }
      },
      onClose: () => setConnected(false),
    });
    socketRef.current = socket;
    return () => {
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
        timerRef.current = null;
      }
      socket.close();
      socketRef.current = null;
    };
  }, [socketFactory, url]);

  const send = useCallback((value: string) => {
    const seq = (seqRef.current += 1);
    socketRef.current?.send(encodeQueryFrame({ q: value, seq }));
  }, []);

  const setQuery = useCallback(
    (value: string) => {
      setQueryState(value);
      if (timerRef.current !== null) {
        clearTimeout(timerRef.current);
      }
      timerRef.current = setTimeout(() => {
        timerRef.current = null;
        send(value);
      }, debounceMs);
    },
    [send, debounceMs],
  );

  return {
    query,
    hits: view.hits,
    error: view.error,
    connected,
    setQuery,
  };
}
