import { describe, expect, test, vi } from "vitest";
import {
  applyPlaybackCommand,
  resolvePlaybackCommand,
  type PlaybackMedia,
} from "./keyboard";

function keyEvent(
  key: string,
  overrides: Partial<{
    ctrlKey: boolean;
    metaKey: boolean;
    altKey: boolean;
    target: EventTarget | null;
  }> = {},
) {
  return {
    key,
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    target: null,
    ...overrides,
  };
}

describe("resolvePlaybackCommand", () => {
  test.each([
    [" ", { kind: "toggle-play" }],
    ["k", { kind: "toggle-play" }],
    ["ArrowLeft", { kind: "seek-by", seconds: -5 }],
    ["ArrowRight", { kind: "seek-by", seconds: 5 }],
    ["j", { kind: "seek-by", seconds: -10 }],
    ["l", { kind: "seek-by", seconds: 10 }],
    ["ArrowUp", { kind: "volume-by", delta: 0.1 }],
    ["ArrowDown", { kind: "volume-by", delta: -0.1 }],
    ["m", { kind: "toggle-mute" }],
    ["f", { kind: "toggle-fullscreen" }],
    ["K", { kind: "toggle-play" }],
  ])("maps %j to %j", (key, expected) => {
    expect(resolvePlaybackCommand(keyEvent(key))).toEqual(expected);
  });

  test("ignores unrelated keys", () => {
    expect(resolvePlaybackCommand(keyEvent("x"))).toBeNull();
    expect(resolvePlaybackCommand(keyEvent("Enter"))).toBeNull();
  });

  test("ignores chords with modifier keys", () => {
    expect(
      resolvePlaybackCommand(keyEvent("k", { ctrlKey: true })),
    ).toBeNull();
    expect(resolvePlaybackCommand(keyEvent("f", { metaKey: true }))).toBeNull();
    expect(resolvePlaybackCommand(keyEvent(" ", { altKey: true }))).toBeNull();
  });

  test("ignores keys sent to interactive elements", () => {
    const interactive: HTMLElement[] = [
      document.createElement("input"),
      document.createElement("textarea"),
      document.createElement("select"),
      document.createElement("button"),
      document.createElement("video"),
      document.createElement("audio"),
    ];
    const editable = document.createElement("div");
    editable.contentEditable = "true";
    interactive.push(editable);
    const link = document.createElement("a");
    link.href = "/somewhere";
    interactive.push(link);
    const focusable = document.createElement("div");
    focusable.tabIndex = 0;
    interactive.push(focusable);
    const widget = document.createElement("div");
    widget.setAttribute("role", "slider");
    interactive.push(widget);

    for (const target of interactive) {
      expect(resolvePlaybackCommand(keyEvent(" ", { target }))).toBeNull();
      expect(resolvePlaybackCommand(keyEvent("k", { target }))).toBeNull();
      expect(
        resolvePlaybackCommand(keyEvent("ArrowUp", { target })),
      ).toBeNull();
    }
  });

  test("handles keys sent to non-interactive targets", () => {
    const section = document.createElement("section");

    expect(resolvePlaybackCommand(keyEvent("k", { target: null }))).toEqual({
      kind: "toggle-play",
    });
    expect(
      resolvePlaybackCommand(keyEvent("k", { target: document.body })),
    ).toEqual({ kind: "toggle-play" });
    expect(
      resolvePlaybackCommand(keyEvent("k", { target: section })),
    ).toEqual({ kind: "toggle-play" });
  });
});

function fakeMedia(overrides: Partial<PlaybackMedia> = {}): PlaybackMedia {
  return {
    paused: true,
    currentTime: 60,
    duration: 300,
    volume: 0.5,
    muted: false,
    play: vi.fn(),
    pause: vi.fn(),
    ...overrides,
  };
}

describe("applyPlaybackCommand", () => {
  test("toggle-play plays when paused and pauses when playing", () => {
    const pausedMedia = fakeMedia({ paused: true });
    applyPlaybackCommand(pausedMedia, { kind: "toggle-play" });
    expect(pausedMedia.play).toHaveBeenCalledOnce();

    const playingMedia = fakeMedia({ paused: false });
    applyPlaybackCommand(playingMedia, { kind: "toggle-play" });
    expect(playingMedia.pause).toHaveBeenCalledOnce();
  });

  test("toggle-play swallows play rejections", async () => {
    const media = fakeMedia({
      paused: true,
      play: vi.fn(() => Promise.reject(new Error("AbortError"))),
    });

    applyPlaybackCommand(media, { kind: "toggle-play" });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(media.play).toHaveBeenCalledOnce();
  });

  test("seek-by moves currentTime and clamps to the media bounds", () => {
    const media = fakeMedia({ currentTime: 3 });
    applyPlaybackCommand(media, { kind: "seek-by", seconds: -5 });
    expect(media.currentTime).toBe(0);

    const nearEnd = fakeMedia({ currentTime: 298 });
    applyPlaybackCommand(nearEnd, { kind: "seek-by", seconds: 10 });
    expect(nearEnd.currentTime).toBe(300);
  });

  test("seek-by without a known duration still seeks forward", () => {
    const media = fakeMedia({ currentTime: 10, duration: Number.NaN });
    applyPlaybackCommand(media, { kind: "seek-by", seconds: 5 });
    expect(media.currentTime).toBe(15);
  });

  test("volume-by clamps between 0 and 1", () => {
    const quiet = fakeMedia({ volume: 0.05 });
    applyPlaybackCommand(quiet, { kind: "volume-by", delta: -0.1 });
    expect(quiet.volume).toBe(0);

    const loud = fakeMedia({ volume: 0.95 });
    applyPlaybackCommand(loud, { kind: "volume-by", delta: 0.1 });
    expect(loud.volume).toBe(1);
  });

  test("raising the volume unmutes", () => {
    const media = fakeMedia({ muted: true, volume: 0.5 });
    applyPlaybackCommand(media, { kind: "volume-by", delta: 0.1 });
    expect(media.muted).toBe(false);

    const lowered = fakeMedia({ muted: true, volume: 0.5 });
    applyPlaybackCommand(lowered, { kind: "volume-by", delta: -0.1 });
    expect(lowered.muted).toBe(true);
  });

  test("toggle-mute flips the muted flag", () => {
    const media = fakeMedia({ muted: false });
    applyPlaybackCommand(media, { kind: "toggle-mute" });
    expect(media.muted).toBe(true);
    applyPlaybackCommand(media, { kind: "toggle-mute" });
    expect(media.muted).toBe(false);
  });
});
