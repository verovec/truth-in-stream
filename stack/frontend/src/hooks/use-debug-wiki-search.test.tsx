import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import type { LiveSocket, LiveSocketHandlers } from "@/lib/live/ports";

import { useDebugWikiSearch } from "./use-debug-wiki-search";

function fakeSocket() {
  const sent: string[] = [];
  let handlers: LiveSocketHandlers | null = null;
  const factory = (_url: string, h: LiveSocketHandlers): LiveSocket => {
    handlers = h;
    return {
      send: (frame) => {
        sent.push(frame as string);
      },
      close: () => {},
    };
  };
  return {
    factory,
    sent,
    open: () => handlers?.onOpen(),
    frame: (raw: string) => handlers?.onFrame(raw),
    closePeer: (clean: boolean) => handlers?.onClose(clean),
  };
}

function resultsFrame(seq: number, title: string, similarity = 0.5): string {
  return JSON.stringify({
    type: "results",
    seq,
    hits: [{ title, url: `https://example.test/${title}`, snippet: title, similarity }],
  });
}

afterEach(() => {
  vi.useRealTimers();
});

describe("useDebugWikiSearch", () => {
  test("debounces a burst of keystrokes into one query frame with the latest text", () => {
    vi.useFakeTimers();
    const sock = fakeSocket();
    const { result } = renderHook(() =>
      useDebugWikiSearch({ socketFactory: sock.factory, url: "ws://test", debounceMs: 250 }),
    );
    act(() => sock.open());

    act(() => result.current.setQuery("f"));
    act(() => result.current.setQuery("fo"));
    act(() => result.current.setQuery("fox"));
    expect(sock.sent).toHaveLength(0);

    act(() => {
      vi.advanceTimersByTime(250);
    });
    expect(sock.sent).toEqual(['{"q":"fox","seq":1}']);
  });

  test("increments the sequence number across successive debounced sends", () => {
    vi.useFakeTimers();
    const sock = fakeSocket();
    const { result } = renderHook(() =>
      useDebugWikiSearch({ socketFactory: sock.factory, url: "ws://test", debounceMs: 100 }),
    );
    act(() => sock.open());

    act(() => result.current.setQuery("a"));
    act(() => {
      vi.advanceTimersByTime(100);
    });
    act(() => result.current.setQuery("ab"));
    act(() => {
      vi.advanceTimersByTime(100);
    });
    expect(sock.sent).toEqual(['{"q":"a","seq":1}', '{"q":"ab","seq":2}']);
  });

  test("renders the newest response and ignores a superseded one", () => {
    const sock = fakeSocket();
    const { result } = renderHook(() =>
      useDebugWikiSearch({ socketFactory: sock.factory, url: "ws://test" }),
    );
    act(() => sock.open());

    act(() => sock.frame(resultsFrame(2, "new")));
    expect(result.current.hits.map((h) => h.title)).toEqual(["new"]);

    // A response from an earlier query lands late; its lower seq must be dropped.
    act(() => sock.frame(resultsFrame(1, "old")));
    expect(result.current.hits.map((h) => h.title)).toEqual(["new"]);
  });

  test("tracks connection state from open and close", () => {
    const sock = fakeSocket();
    const { result } = renderHook(() =>
      useDebugWikiSearch({ socketFactory: sock.factory, url: "ws://test" }),
    );
    expect(result.current.connected).toBe(false);

    act(() => sock.open());
    expect(result.current.connected).toBe(true);

    act(() => sock.closePeer(true));
    expect(result.current.connected).toBe(false);
  });

  test("surfaces an in-band search error", () => {
    const sock = fakeSocket();
    const { result } = renderHook(() =>
      useDebugWikiSearch({ socketFactory: sock.factory, url: "ws://test" }),
    );
    act(() => sock.open());

    act(() =>
      sock.frame(JSON.stringify({ type: "results", seq: 1, hits: [], error: "search failed" })),
    );
    expect(result.current.error).toBe("search failed");
    expect(result.current.hits).toEqual([]);
  });
});
