import { afterEach, describe, expect, test, vi } from "vitest";
import nextConfig from "./next.config";

describe("next.config rewrites", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  test("proxies /api and /demo to the backend from BACKEND_URL", async () => {
    vi.stubEnv("BACKEND_URL", "http://backend:8080");

    const rewrites = await nextConfig.rewrites?.();

    expect(rewrites).toEqual([
      {
        source: "/api/:path*",
        destination: "http://backend:8080/api/:path*",
      },
      {
        source: "/demo/:path*",
        destination: "http://backend:8080/demo/:path*",
      },
    ]);
  });

  test("defaults to localhost for local dev outside compose", async () => {
    vi.stubEnv("BACKEND_URL", "");

    const rewrites = await nextConfig.rewrites?.();

    expect(rewrites).toEqual([
      {
        source: "/api/:path*",
        destination: "http://localhost:8080/api/:path*",
      },
      {
        source: "/demo/:path*",
        destination: "http://localhost:8080/demo/:path*",
      },
    ]);
  });
});
