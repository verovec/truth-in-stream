// @vitest-environment node
import { describe, expect, test, vi } from "vitest";

vi.mock("server-only", () => ({}));
vi.mock("next/headers", () => ({ cookies: vi.fn() }));

import { GUEST_SESSION, resolveSession } from "./session";

function token(payload: Record<string, unknown>): string {
  const b64 = (value: string) =>
    Buffer.from(value, "utf8")
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  return `${b64('{"alg":"RS256"}')}.${b64(JSON.stringify(payload))}.sig`;
}

describe("resolveSession", () => {
  test("is a guest with no token", () => {
    expect(resolveSession(undefined)).toEqual(GUEST_SESSION);
  });

  test("authenticates a live admin token as admin", () => {
    const t = token({ exp: 1000, realm_access: { roles: ["admin", "guest"] } });

    expect(resolveSession(t, 100)).toEqual({
      authenticated: true,
      role: "admin",
    });
  });

  test("authenticates a live guest token as guest", () => {
    const t = token({ exp: 1000, realm_access: { roles: ["guest"] } });

    expect(resolveSession(t, 100)).toEqual({
      authenticated: true,
      role: "guest",
    });
  });

  test("treats an expired token as no session, never falling back to a stale role", () => {
    const t = token({ exp: 1000, realm_access: { roles: ["admin"] } });

    expect(resolveSession(t, 2000)).toEqual(GUEST_SESSION);
  });
});
