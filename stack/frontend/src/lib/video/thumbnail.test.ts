import { beforeEach, describe, expect, test, vi } from "vitest";
import {
  loadThumbnailSource,
  resetThumbnailSourceCache,
  seekTarget,
} from "./thumbnail";

describe("seekTarget", () => {
  test("targets the seek offset for a long clip", () => {
    expect(seekTarget(60)).toBe(2);
  });

  test("stays within the first half of a short clip", () => {
    expect(seekTarget(3)).toBe(1.5);
    expect(seekTarget(0.5)).toBe(0.25);
  });

  test("returns 0 for an unknown or empty duration", () => {
    expect(seekTarget(0)).toBe(0);
    expect(seekTarget(Number.NaN)).toBe(0);
    expect(seekTarget(Number.POSITIVE_INFINITY)).toBe(0);
  });
});

function playback(url: string) {
  return { playback: { url } };
}

describe("loadThumbnailSource", () => {
  beforeEach(() => {
    resetThumbnailSourceCache();
  });

  test("fetches the playback URL once per id and caches it", async () => {
    const getPlayback = vi.fn().mockResolvedValue(playback("frame-a"));

    await expect(loadThumbnailSource("a", getPlayback)).resolves.toBe("frame-a");
    await expect(loadThumbnailSource("a", getPlayback)).resolves.toBe("frame-a");

    expect(getPlayback).toHaveBeenCalledTimes(1);
  });

  test("fetches distinct ids independently", async () => {
    const getPlayback = vi
      .fn()
      .mockImplementation((id: string) => Promise.resolve(playback(`frame-${id}`)));

    await expect(loadThumbnailSource("a", getPlayback)).resolves.toBe("frame-a");
    await expect(loadThumbnailSource("b", getPlayback)).resolves.toBe("frame-b");

    expect(getPlayback).toHaveBeenCalledTimes(2);
  });

  test("evicts a failed fetch so a later attempt retries", async () => {
    const getPlayback = vi
      .fn()
      .mockRejectedValueOnce(new Error("boom"))
      .mockResolvedValueOnce(playback("frame-a"));

    await expect(loadThumbnailSource("a", getPlayback)).rejects.toThrow("boom");
    await expect(loadThumbnailSource("a", getPlayback)).resolves.toBe("frame-a");

    expect(getPlayback).toHaveBeenCalledTimes(2);
  });
});
