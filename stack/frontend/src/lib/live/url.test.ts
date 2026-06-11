import { describe, expect, test } from "vitest";
import { liveSocketUrl } from "./url";

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
