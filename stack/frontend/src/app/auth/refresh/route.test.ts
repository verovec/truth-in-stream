// @vitest-environment node
import { beforeEach, describe, expect, test, vi } from "vitest";

const store = createCookieStore();
vi.mock("next/headers", () => ({ cookies: () => Promise.resolve(store) }));
vi.mock("@/lib/auth/oidc", () => ({ refreshTokens: vi.fn() }));

import { refreshTokens } from "@/lib/auth/oidc";
import { POST } from "./route";

function liveToken(): string {
  const b64 = (v: string) =>
    Buffer.from(v, "utf8")
      .toString("base64")
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/, "");
  const exp = Math.floor(Date.now() / 1000) + 3600;
  return `${b64('{"alg":"RS256"}')}.${b64(
    JSON.stringify({ exp, realm_access: { roles: ["guest"] } }),
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

describe("auth refresh", () => {
  test("returns 401 when there is no refresh token", async () => {
    const res = await POST();

    expect(res.status).toBe(401);
    expect(refreshTokens).not.toHaveBeenCalled();
  });

  test("refreshes the access token and re-sets the session cookies", async () => {
    store.set("kc_refresh", "old-refresh");
    vi.mocked(refreshTokens).mockResolvedValue({
      accessToken: liveToken(),
      refreshToken: "new-refresh",
    });

    const res = await POST();

    expect(res.status).toBe(200);
    expect(store.get("kc_access")?.value).toBeTruthy();
    expect(store.get("kc_refresh")?.value).toBe("new-refresh");
  });

  test("clears the dead session and reports 401 when the refresh is rejected", async () => {
    store.set("kc_access", "stale");
    store.set("kc_refresh", "expired-refresh");
    store.set("kc_id", "id");
    vi.mocked(refreshTokens).mockRejectedValue(new Error("invalid_grant"));

    const res = await POST();

    expect(res.status).toBe(401);
    expect(store.get("kc_access")).toBeUndefined();
    expect(store.get("kc_refresh")).toBeUndefined();
    expect(store.get("kc_id")).toBeUndefined();
  });
});
