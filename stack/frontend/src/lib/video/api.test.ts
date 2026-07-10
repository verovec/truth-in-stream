import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import {
  confirmVideo,
  deleteVideo,
  getVideo,
  listVideos,
  requestUpload,
  submitYoutubeUrl,
} from "./api";

function mockFetch(status: number, body: unknown) {
  return vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

afterEach(() => vi.restoreAllMocks());

const sampleWire = {
  id: "vid-1",
  title: "Common Myths",
  status: "ready",
  kind: "sample",
  content_type: "video/mp4",
  size_bytes: 0,
  created_at: "2026-06-10T18:00:00Z",
  updated_at: "2026-06-10T18:00:00Z",
};

const sampleVideo = {
  id: "vid-1",
  title: "Common Myths",
  status: "ready",
  kind: "sample",
  contentType: "video/mp4",
  sizeBytes: 0,
  createdAt: "2026-06-10T18:00:00Z",
  updatedAt: "2026-06-10T18:00:00Z",
};

describe("listVideos", () => {
  test("returns mapped videos in served order", async () => {
    const fetchSpy = mockFetch(200, {
      videos: [
        sampleWire,
        {
          id: "vid-2",
          title: "My Upload",
          status: "pending",
          kind: "upload",
          content_type: "video/webm",
          size_bytes: 2048,
          created_at: "2026-06-11T09:00:00Z",
          updated_at: "2026-06-11T09:00:00Z",
        },
      ],
    });

    const videos = await listVideos();

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos", { signal: undefined });
    expect(videos).toEqual([
      sampleVideo,
      {
        id: "vid-2",
        title: "My Upload",
        status: "pending",
        kind: "upload",
        contentType: "video/webm",
        sizeBytes: 2048,
        createdAt: "2026-06-11T09:00:00Z",
        updatedAt: "2026-06-11T09:00:00Z",
      },
    ]);
  });

  test("treats a missing videos array as empty", async () => {
    mockFetch(200, {});

    await expect(listVideos()).resolves.toEqual([]);
  });

  test("throws an ApiError on server failure", async () => {
    mockFetch(500, { error: "internal error" });

    await expect(listVideos()).rejects.toThrow(
      new ApiError("internal error", 500),
    );
  });
});

describe("getVideo", () => {
  test("returns the record with a presigned playback request", async () => {
    const fetchSpy = mockFetch(200, {
      ...sampleWire,
      playback: {
        url: "https://storage.example/samples/common-myths.mp4?sig=abc",
        method: "GET",
        headers: { Host: ["storage.example"] },
      },
    });

    const playable = await getVideo("vid-1");

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/vid-1", {
      signal: undefined,
    });
    expect(playable).toEqual({
      ...sampleVideo,
      playback: {
        url: "https://storage.example/samples/common-myths.mp4?sig=abc",
        method: "GET",
        headers: { Host: ["storage.example"] },
      },
    });
  });

  test("encodes the id and throws an ApiError for an unknown video", async () => {
    const fetchSpy = mockFetch(404, { error: "unknown video" });

    await expect(getVideo("a/b")).rejects.toThrow(
      new ApiError("unknown video", 404),
    );
    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/a%2Fb", {
      signal: undefined,
    });
  });
});

describe("requestUpload", () => {
  test("posts snake_case metadata and maps the ticket", async () => {
    const fetchSpy = mockFetch(201, {
      video_id: "vid-9",
      object_key: "uploads/vid-9.mp4",
      status: "pending",
      upload: {
        url: "https://storage.example/uploads/vid-9.mp4?sig=put",
        method: "PUT",
        headers: { "Content-Type": ["video/mp4"] },
      },
    });

    const ticket = await requestUpload({
      title: "Clip",
      contentType: "video/mp4",
      sizeBytes: 1234,
    });

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/uploads", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: "Clip",
        content_type: "video/mp4",
        size_bytes: 1234,
      }),
      signal: undefined,
    });
    expect(ticket).toEqual({
      videoId: "vid-9",
      objectKey: "uploads/vid-9.mp4",
      status: "pending",
      upload: {
        url: "https://storage.example/uploads/vid-9.mp4?sig=put",
        method: "PUT",
        headers: { "Content-Type": ["video/mp4"] },
      },
    });
  });

  test("throws an ApiError carrying the backend message", async () => {
    mockFetch(415, { error: "unsupported content type" });

    await expect(
      requestUpload({ title: "x", contentType: "video/avi", sizeBytes: 1 }),
    ).rejects.toThrow(new ApiError("unsupported content type", 415));
  });
});

describe("submitYoutubeUrl", () => {
  test("posts the url and maps the accepted pending record", async () => {
    const fetchSpy = mockFetch(202, {
      id: "vid-yt",
      title: "https://www.youtube.com/watch?v=abc",
      status: "pending",
      kind: "youtube",
      content_type: "video/mp4",
      size_bytes: 0,
      created_at: "2026-06-11T12:00:00Z",
      updated_at: "2026-06-11T12:00:00Z",
    });

    const video = await submitYoutubeUrl(
      "https://www.youtube.com/watch?v=abc",
    );

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/youtube", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ url: "https://www.youtube.com/watch?v=abc" }),
      signal: undefined,
    });
    expect(video).toEqual({
      id: "vid-yt",
      title: "https://www.youtube.com/watch?v=abc",
      status: "pending",
      kind: "youtube",
      contentType: "video/mp4",
      sizeBytes: 0,
      createdAt: "2026-06-11T12:00:00Z",
      updatedAt: "2026-06-11T12:00:00Z",
    });
  });

  test("throws an ApiError carrying the backend rejection message", async () => {
    mockFetch(400, { error: "not a valid youtube video url" });

    await expect(submitYoutubeUrl("nope")).rejects.toThrow(
      new ApiError("not a valid youtube video url", 400),
    );
  });
});

describe("confirmVideo", () => {
  test("posts to the confirm route and maps the ready record", async () => {
    const fetchSpy = mockFetch(200, { ...sampleWire, status: "ready" });

    const video = await confirmVideo("vid-9");

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/vid-9/confirm", {
      method: "POST",
      signal: undefined,
    });
    expect(video.status).toBe("ready");
  });

  test("throws an ApiError with 409 when the object is not yet in storage", async () => {
    mockFetch(409, { error: "upload not found in storage" });

    await expect(confirmVideo("vid-9")).rejects.toThrow(
      new ApiError("upload not found in storage", 409),
    );
  });
});

describe("deleteVideo", () => {
  test("issues a DELETE and resolves on a 204 without parsing a body", async () => {
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 204 }));

    await expect(deleteVideo("vid-9")).resolves.toBeUndefined();

    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/vid-9", {
      method: "DELETE",
      signal: undefined,
    });
  });

  test("encodes the id and throws an ApiError carrying the backend message", async () => {
    const fetchSpy = mockFetch(404, { error: "unknown video" });

    await expect(deleteVideo("a/b")).rejects.toThrow(
      new ApiError("unknown video", 404),
    );
    expect(fetchSpy).toHaveBeenCalledWith("/api/videos/a%2Fb", {
      method: "DELETE",
      signal: undefined,
    });
  });
});
