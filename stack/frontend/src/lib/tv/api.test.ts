import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError } from "@/lib/http";
import {
  createChannel,
  deleteChannel,
  listChannels,
  updateChannel,
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
  id: "chan-1",
  slug: "france-24",
  name: "France 24",
  source_kind: "youtube",
  source_ref: "https://www.youtube.com/@FRANCE24/live",
  enabled: true,
  archive_enabled: false,
  live: true,
};

const sampleChannel = {
  id: "chan-1",
  slug: "france-24",
  name: "France 24",
  sourceKind: "youtube",
  sourceRef: "https://www.youtube.com/@FRANCE24/live",
  enabled: true,
  archiveEnabled: false,
  live: true,
};

describe("listChannels", () => {
  test("returns mapped channels in served order", async () => {
    const fetchSpy = mockFetch(200, {
      channels: [
        sampleWire,
        {
          id: "chan-2",
          slug: "public-senat",
          name: "Public Sénat",
          source_kind: "hls",
          source_ref: "https://stream.example/senat.m3u8",
          enabled: false,
          archive_enabled: true,
          live: false,
        },
      ],
    });

    const channels = await listChannels();

    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels", {
      signal: undefined,
    });
    expect(channels).toEqual([
      sampleChannel,
      {
        id: "chan-2",
        slug: "public-senat",
        name: "Public Sénat",
        sourceKind: "hls",
        sourceRef: "https://stream.example/senat.m3u8",
        enabled: false,
        archiveEnabled: true,
        live: false,
      },
    ]);
  });

  test("treats a missing channels array as empty", async () => {
    mockFetch(200, {});
    await expect(listChannels()).resolves.toEqual([]);
  });

  test("throws an ApiError on server failure", async () => {
    mockFetch(500, { error: "internal error" });
    await expect(listChannels()).rejects.toThrow(
      new ApiError("internal error", 500),
    );
  });
});

describe("createChannel", () => {
  test("posts snake_case fields and maps the created channel", async () => {
    const fetchSpy = mockFetch(201, sampleWire);

    const channel = await createChannel({
      slug: "france-24",
      name: "France 24",
      sourceKind: "youtube",
      sourceRef: "https://www.youtube.com/@FRANCE24/live",
      enabled: true,
    });

    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        slug: "france-24",
        name: "France 24",
        source_kind: "youtube",
        source_ref: "https://www.youtube.com/@FRANCE24/live",
        enabled: true,
      }),
      signal: undefined,
    });
    expect(channel).toEqual(sampleChannel);
  });

  test("omits optional flags when not supplied", async () => {
    const fetchSpy = mockFetch(201, sampleWire);

    await createChannel({
      slug: "france-24",
      name: "France 24",
      sourceKind: "youtube",
      sourceRef: "https://www.youtube.com/@FRANCE24/live",
    });

    const init = fetchSpy.mock.calls[0][1];
    expect(JSON.parse(String(init?.body))).toEqual({
      slug: "france-24",
      name: "France 24",
      source_kind: "youtube",
      source_ref: "https://www.youtube.com/@FRANCE24/live",
    });
  });

  test("throws an ApiError carrying the backend duplicate-slug message", async () => {
    mockFetch(409, { error: "slug already exists" });
    await expect(
      createChannel({
        slug: "france-24",
        name: "France 24",
        sourceKind: "youtube",
        sourceRef: "https://www.youtube.com/@FRANCE24/live",
      }),
    ).rejects.toThrow(new ApiError("slug already exists", 409));
  });
});

describe("updateChannel", () => {
  test("patches only the supplied keys and maps the result", async () => {
    const fetchSpy = mockFetch(200, { ...sampleWire, enabled: false });

    const channel = await updateChannel("chan-1", { enabled: false });

    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels/chan-1", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: false }),
      signal: undefined,
    });
    expect(channel.enabled).toBe(false);
  });

  test("translates every mutable field to snake_case", async () => {
    const fetchSpy = mockFetch(200, sampleWire);

    await updateChannel("chan-1", {
      name: "France 24 HD",
      sourceKind: "hls",
      sourceRef: "https://stream.example/f24.m3u8",
      enabled: true,
      archiveEnabled: true,
    });

    const init = fetchSpy.mock.calls[0][1];
    expect(JSON.parse(String(init?.body))).toEqual({
      name: "France 24 HD",
      source_kind: "hls",
      source_ref: "https://stream.example/f24.m3u8",
      enabled: true,
      archive_enabled: true,
    });
  });

  test("encodes the id and throws an ApiError for an unknown channel", async () => {
    const fetchSpy = mockFetch(404, { error: "unknown channel" });

    await expect(
      updateChannel("a/b", { archiveEnabled: true }),
    ).rejects.toThrow(new ApiError("unknown channel", 404));
    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels/a%2Fb", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ archive_enabled: true }),
      signal: undefined,
    });
  });
});

describe("deleteChannel", () => {
  test("issues a DELETE and resolves on a 204 without parsing a body", async () => {
    const fetchSpy = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 204 }));

    await expect(deleteChannel("chan-1")).resolves.toBeUndefined();

    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels/chan-1", {
      method: "DELETE",
      signal: undefined,
    });
  });

  test("encodes the id and throws an ApiError carrying the backend message", async () => {
    const fetchSpy = mockFetch(404, { error: "unknown channel" });

    await expect(deleteChannel("a/b")).rejects.toThrow(
      new ApiError("unknown channel", 404),
    );
    expect(fetchSpy).toHaveBeenCalledWith("/api/tv/channels/a%2Fb", {
      method: "DELETE",
      signal: undefined,
    });
  });
});
