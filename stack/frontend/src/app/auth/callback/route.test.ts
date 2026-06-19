// @vitest-environment node
import { NextRequest } from "next/server";
import { beforeEach, describe, expect, test, vi } from "vitest";

const store = createCookieStore();
vi.mock("next/headers", () => ({ cookies: () => Promise.resolve(store) }));
vi.mock("@/lib/auth/oidc", () => ({ exchangeCode: vi.fn() }));

import { exchangeCode } from "@/lib/auth/oidc";
import { GET } from "./route";

function liveToken(role: string): string {
  const b64 = (v: string) =>
    Buffer.from(v, "utf8")
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  const exp = Math.floor(Date.now() / 1000) + 3600;
  return `${b64('{"alg":"RS256"}')}.${b64(
    JSON.stringify({ exp, realm_access: { roles: [role] } }),
  )}.sig`;
}

function createCookieStore() {
  const map = new Map<string, string>();
  return {
    get: (name: string) =>
      map.has(name) ? { name, value: map.get(name)! } : undefined,
    set: (name: string, value: string) => map.set(name, value),
    delete: (name: string) => map.delete(name),
    _map: map,
  };
}

beforeEach(() => {
  store._map.clear();
});

describe("auth callback", () => {
  test("redirects back to login when the PKCE transaction cookies are missing", async () => {
    const res = await GET(
      new NextRequest("http://localhost:3000/auth/callback?code=abc&state=s"),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toContain("/login?error=session");
    expect(exchangeCode).not.toHaveBeenCalled();
  });

  test("exchanges the code, sets the session cookies, and lands in the app", async () => {
    store.set("kc_pkce_verifier", "verifier");
    store.set("kc_oauth_state", "state-123");
    vi.mocked(exchangeCode).mockResolvedValue({
      accessToken: liveToken("admin"),
      refreshToken: "refresh-token",
      idToken: "id-token",
    });

    const res = await GET(
      new NextRequest(
        "http://localhost:3000/auth/callback?code=abc&state=state-123",
      ),
    );

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toContain("/app");
    expect(store.get("kc_access")?.value).toBeTruthy();
    expect(store.get("kc_refresh")?.value).toBe("refresh-token");
    expect(store.get("kc_id")?.value).toBe("id-token");
    // Transaction cookies are cleared after the exchange.
    expect(store.get("kc_pkce_verifier")).toBeUndefined();
    expect(store.get("kc_oauth_state")).toBeUndefined();
  });

  test("redirects to login with an error when the code exchange fails", async () => {
    store.set("kc_pkce_verifier", "verifier");
    store.set("kc_oauth_state", "state-123");
    vi.mocked(exchangeCode).mockRejectedValue(new Error("bad code"));

    const res = await GET(
      new NextRequest(
        "http://localhost:3000/auth/callback?code=abc&state=state-123",
      ),
    );

    expect(res.headers.get("location")).toContain("/login?error=exchange");
    expect(store.get("kc_access")).toBeUndefined();
  });
});
