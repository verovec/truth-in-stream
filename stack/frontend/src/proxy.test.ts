// @vitest-environment node
import { NextRequest } from "next/server";
import { describe, expect, test } from "vitest";
import { config, proxy } from "./proxy";

function request(path: string, sessionCookie?: string) {
  const headers = sessionCookie
    ? { cookie: `kc_access=${sessionCookie}` }
    : undefined;
  return new NextRequest(`http://localhost:3000${path}`, { headers });
}

describe("proxy gating", () => {
  test("lets an unauthenticated request reach the public landing page", () => {
    const res = proxy(request("/"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("lets an unauthenticated request reach the localised landing pages", () => {
    for (const path of ["/fr", "/en"]) {
      const res = proxy(request(path));
      expect(res.headers.get("x-middleware-next")).toBe("1");
    }
  });

  test("redirects an unauthenticated request for a product route to login", () => {
    const res = proxy(request("/app"));

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("http://localhost:3000/login");
  });

  test("lets an authenticated request reach a product route", () => {
    const res = proxy(request("/app", "sometoken"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("accepts the backend session cookie as a valid session for navigation", () => {
    const req = new NextRequest("http://localhost:3000/app", {
      headers: { cookie: "session=backendtoken" },
    });
    const res = proxy(req);

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("lets an unauthenticated request reach the login page", () => {
    const res = proxy(request("/login"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("lets the unauthenticated OIDC routes through so a sign-in can start and complete", () => {
    for (const path of ["/auth/login", "/auth/callback", "/auth/logout"]) {
      const res = proxy(request(path));
      expect(res.headers.get("x-middleware-next")).toBe("1");
    }
  });

  test("keeps the login page reachable with a cookie present, so a stale cookie cannot lock the user out", () => {
    const res = proxy(request("/login", "staletoken"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("does not redirect an unknown unauthenticated route under /auth (no blanket prefix allow)", () => {
    const res = proxy(request("/auth/secret"));

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("http://localhost:3000/login");
  });

  test("lets an unauthenticated request reach a static marketing asset", () => {
    const res = proxy(request("/og.png"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });
});

describe("proxy bearer promotion", () => {
  test("promotes the access-token cookie to an Authorization bearer header on /api", () => {
    const res = proxy(request("/api/videos", "the-token"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
    expect(res.headers.get("x-middleware-override-headers")).toContain(
      "authorization",
    );
    expect(res.headers.get("x-middleware-request-authorization")).toBe(
      "Bearer the-token",
    );
  });

  test("forwards an /api request unchanged when there is no session cookie", () => {
    const res = proxy(request("/api/videos"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
    expect(res.headers.get("x-middleware-request-authorization")).toBeNull();
  });
});

describe("proxy matcher", () => {
  test("runs on /api so the bearer promotion can fire, and excludes framework assets", () => {
    expect(config.matcher).toBeDefined();
    const [pattern] = config.matcher;
    expect(pattern).not.toContain("api/)");
    expect(pattern).toContain("_next");
  });
});
