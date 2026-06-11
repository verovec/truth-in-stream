import { act, render, screen, waitFor } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import type { LibraryVideo, PlayableVideo } from "@/lib/video/api";
import { resetThumbnailSourceCache } from "@/lib/video/thumbnail";
import { useVideoThumbnail } from "./use-video-thumbnail";

function video(overrides: Partial<LibraryVideo> = {}): LibraryVideo {
  return {
    id: "vid-1",
    title: "Common Myths",
    status: "ready",
    kind: "sample",
    contentType: "video/mp4",
    sizeBytes: 0,
    createdAt: "2026-06-10T18:00:00Z",
    updatedAt: "2026-06-10T18:00:00Z",
    ...overrides,
  };
}

function playable(url: string): PlayableVideo {
  return {
    ...video(),
    playback: { url, method: "GET", headers: {} },
  };
}

let observers: MockIntersectionObserver[] = [];

class MockIntersectionObserver {
  readonly callback: IntersectionObserverCallback;
  observe = vi.fn();
  disconnect = vi.fn();
  unobserve = vi.fn();
  takeRecords = vi.fn(() => []);

  constructor(callback: IntersectionObserverCallback) {
    this.callback = callback;
    observers.push(this);
  }

  enter() {
    this.callback(
      [{ isIntersecting: true } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }

  leave() {
    this.callback(
      [{ isIntersecting: false } as IntersectionObserverEntry],
      this as unknown as IntersectionObserver,
    );
  }
}

function Harness({
  video: input,
  getPlayback,
}: {
  video: LibraryVideo;
  getPlayback: (id: string, signal?: AbortSignal) => Promise<PlayableVideo>;
}) {
  const { ref, src } = useVideoThumbnail<HTMLDivElement>({ video: input, getPlayback });
  return createElement("div", { ref, "data-testid": "tile" }, src ?? "no-src");
}

describe("useVideoThumbnail", () => {
  beforeEach(() => {
    observers = [];
    resetThumbnailSourceCache();
    vi.stubGlobal("IntersectionObserver", MockIntersectionObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  test("resolves the poster URL once a ready tile enters the viewport", async () => {
    const getPlayback = vi.fn().mockResolvedValue(playable("frame-url"));
    render(createElement(Harness, { video: video(), getPlayback }));

    expect(observers).toHaveLength(1);
    expect(observers[0].observe).toHaveBeenCalledOnce();
    expect(screen.getByTestId("tile")).toHaveTextContent("no-src");

    await act(async () => {
      observers[0].enter();
    });

    await waitFor(() =>
      expect(screen.getByTestId("tile")).toHaveTextContent("frame-url"),
    );
    expect(getPlayback).toHaveBeenCalledWith("vid-1");
  });

  test("does not fetch before the tile is visible", () => {
    const getPlayback = vi.fn().mockResolvedValue(playable("frame-url"));
    render(createElement(Harness, { video: video(), getPlayback }));

    observers[0].leave();
    expect(getPlayback).not.toHaveBeenCalled();
    expect(screen.getByTestId("tile")).toHaveTextContent("no-src");
  });

  test("never observes a non-ready video", () => {
    const getPlayback = vi.fn().mockResolvedValue(playable("frame-url"));
    render(
      createElement(Harness, { video: video({ status: "pending" }), getPlayback }),
    );

    expect(observers).toHaveLength(0);
    expect(getPlayback).not.toHaveBeenCalled();
  });

  test("is a no-op when IntersectionObserver is unavailable", () => {
    vi.stubGlobal("IntersectionObserver", undefined);
    const getPlayback = vi.fn().mockResolvedValue(playable("frame-url"));
    render(createElement(Harness, { video: video(), getPlayback }));

    expect(getPlayback).not.toHaveBeenCalled();
    expect(screen.getByTestId("tile")).toHaveTextContent("no-src");
  });
});
