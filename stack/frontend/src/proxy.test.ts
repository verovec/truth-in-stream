// @vitest-environment node
import { NextRequest } from "next/server";
import { describe, expect, test } from "vitest";
import { config, proxy } from "./proxy";

function request(path: string, sessionCookie?: string) {
  const headers = sessionCookie
    ? { cookie: `session=${sessionCookie}` }
    : undefined;
  return new NextRequest(`http://localhost:3000${path}`, { headers });
}

describe("proxy", () => {
  test("redirects an unauthenticated request to the login page", () => {
    const res = proxy(request("/"));

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("http://localhost:3000/login");
  });

  test("lets a request with a session cookie through", () => {
    const res = proxy(request("/", "sometoken"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("lets an unauthenticated request reach the login page", () => {
    const res = proxy(request("/login"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("keeps the login page reachable with a cookie present, so a stale cookie cannot lock the operator out", () => {
    const res = proxy(request("/login", "staletoken"));

    expect(res.headers.get("x-middleware-next")).toBe("1");
  });

  test("matcher excludes the api proxy and static assets", () => {
    expect(config.matcher).toBeDefined();
    const [pattern] = config.matcher;
    expect(pattern).toContain("api");
    expect(pattern).toContain("_next");
  });
});
