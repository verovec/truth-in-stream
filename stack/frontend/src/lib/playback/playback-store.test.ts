import { describe, expect, test, vi } from "vitest";
import { createPlaybackStore } from "./playback-store";

describe("createPlaybackStore", () => {
  test("starts with an idle snapshot", () => {
    const store = createPlaybackStore();

    expect(store.getSnapshot()).toEqual({
      currentTime: 0,
      duration: 0,
      paused: true,
    });
  });

  test("update merges changes and notifies subscribers", () => {
    const store = createPlaybackStore();
    const listener = vi.fn();
    store.subscribe(listener);

    store.update({ currentTime: 12.5, duration: 300 });

    expect(listener).toHaveBeenCalledTimes(1);
    expect(store.getSnapshot()).toEqual({
      currentTime: 12.5,
      duration: 300,
      paused: true,
    });
  });

  test("returns the same snapshot reference between updates", () => {
    const store = createPlaybackStore();
    store.update({ currentTime: 1 });

    expect(store.getSnapshot()).toBe(store.getSnapshot());
  });

  test("does not notify when nothing changes", () => {
    const store = createPlaybackStore();
    store.update({ currentTime: 5 });
    const listener = vi.fn();
    store.subscribe(listener);

    store.update({ currentTime: 5 });

    expect(listener).not.toHaveBeenCalled();
    expect(store.getSnapshot()).toBe(store.getSnapshot());
  });

  test("unsubscribe stops notifications", () => {
    const store = createPlaybackStore();
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);

    unsubscribe();
    store.update({ currentTime: 3 });

    expect(listener).not.toHaveBeenCalled();
  });

  test("does not notify repeatedly for an unchanged NaN duration", () => {
    const store = createPlaybackStore();
    store.update({ duration: Number.NaN });
    const listener = vi.fn();
    store.subscribe(listener);

    store.update({ duration: Number.NaN });

    expect(listener).not.toHaveBeenCalled();
  });

  test("seekTo forwards to the registered seek handler", () => {
    const store = createPlaybackStore();
    const handler = vi.fn();

    store.seekTo(12);
    const unregister = store.registerSeekHandler(handler);
    store.seekTo(34);

    expect(handler).toHaveBeenCalledExactlyOnceWith(34);

    unregister();
    store.seekTo(56);

    expect(handler).toHaveBeenCalledTimes(1);
  });

  test("registers and exposes the media element, and unregister clears it", () => {
    const store = createPlaybackStore();
    const media = {} as HTMLMediaElement;

    expect(store.getMediaElement()).toBeNull();
    const unregister = store.registerMediaElement(media);
    expect(store.getMediaElement()).toBe(media);

    unregister();
    expect(store.getMediaElement()).toBeNull();
  });

  test("a later media registration supersedes an earlier one", () => {
    const store = createPlaybackStore();
    const first = {} as HTMLMediaElement;
    const second = {} as HTMLMediaElement;
    store.registerMediaElement(first);
    const unregisterSecond = store.registerMediaElement(second);

    expect(store.getMediaElement()).toBe(second);
    // Unregistering the current element clears it; it does not restore the first.
    unregisterSecond();
    expect(store.getMediaElement()).toBeNull();
  });

  test("notifySeeked fans out to seek subscribers until they unsubscribe", () => {
    const store = createPlaybackStore();
    const listener = vi.fn();
    const unsubscribe = store.subscribeSeeked(listener);

    store.notifySeeked();
    expect(listener).toHaveBeenCalledTimes(1);

    unsubscribe();
    store.notifySeeked();
    expect(listener).toHaveBeenCalledTimes(1);
  });

  test("notifies every subscriber once per update", () => {
    const store = createPlaybackStore();
    const first = vi.fn();
    const second = vi.fn();
    store.subscribe(first);
    store.subscribe(second);

    store.update({ paused: false });

    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });
});
