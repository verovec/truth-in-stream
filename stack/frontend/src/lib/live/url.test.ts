import { describe, expect, test } from "vitest";
import { channelLiveSocketUrl, debugSearchUrl, liveSocketUrl } from "./url";

describe("liveSocketUrl", () => {
  test("derives a same-origin ws URL from the page origin when no API base is set", () => {
    expect(liveSocketUrl("vid 1", { apiBase: "", origin: "http://localhost:3000" })).toBe(
      "ws://localhost:3000/api/videos/vid%201/live",
    );
  });

  test("uses wss for a secure page origin", () => {
    expect(
      liveSocketUrl("abc", { apiBase: "", origin: "https://truth.example.com" }),
    ).toBe("wss://truth.example.com/api/videos/abc/live");
  });

  test("converts an http API base to ws", () => {
    expect(
      liveSocketUrl("abc", {
        apiBase: "http://backend:8080",
        origin: "http://localhost:3000",
      }),
    ).toBe("ws://backend:8080/api/videos/abc/live");
  });

  test("converts an https API base to wss", () => {
    expect(
      liveSocketUrl("abc", {
        apiBase: "https://api.example.com",
        origin: "https://truth.example.com",
      }),
    ).toBe("wss://api.example.com/api/videos/abc/live");
  });
});

describe("debugSearchUrl", () => {
  test("derives a same-origin ws URL from the page origin when no API base is set", () => {
    expect(debugSearchUrl({ apiBase: "", origin: "http://localhost:3000" })).toBe(
      "ws://localhost:3000/api/debug/wiki-search",
    );
  });

  test("converts an http API base to ws", () => {
    expect(
      debugSearchUrl({ apiBase: "http://backend:8080", origin: "http://localhost:3000" }),
    ).toBe("ws://backend:8080/api/debug/wiki-search");
  });

  test("uses wss for a secure API base", () => {
    expect(
      debugSearchUrl({
        apiBase: "https://api.example.com",
        origin: "https://truth.example.com",
      }),
    ).toBe("wss://api.example.com/api/debug/wiki-search");
  });
});

describe("channelLiveSocketUrl", () => {
  test("derives a same-origin ws URL to the channel viewer stream", () => {
    expect(
      channelLiveSocketUrl("chan 1", { apiBase: "", origin: "http://localhost:3000" }),
    ).toBe("ws://localhost:3000/api/tv/channels/chan%201/live");
  });

  test("uses wss for a secure page origin and path-encodes the id", () => {
    expect(
      channelLiveSocketUrl("a/b", { apiBase: "", origin: "https://truth.example.com" }),
    ).toBe("wss://truth.example.com/api/tv/channels/a%2Fb/live");
  });

  test("converts an http API base to ws", () => {
    expect(
      channelLiveSocketUrl("abc", {
        apiBase: "http://backend:8080",
        origin: "http://localhost:3000",
      }),
    ).toBe("ws://backend:8080/api/tv/channels/abc/live");
  });
});
