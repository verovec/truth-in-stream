// @vitest-environment node
import { NextRequest } from "next/server";
import { beforeEach, describe, expect, test, vi } from "vitest";

const store = createCookieStore();
vi.mock("next/headers", () => ({ cookies: () => Promise.resolve(store) }));
vi.mock("@/lib/auth/oidc", () => ({ buildLogoutUrl: vi.fn() }));

import { buildLogoutUrl } from "@/lib/auth/oidc";
import { GET } from "./route";

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

describe("auth logout", () => {
  test("clears the session cookies and redirects to the Keycloak end-session endpoint", async () => {
    store.set("kc_access", "a");
    store.set("kc_refresh", "r");
    store.set("kc_id", "id-token");
    vi.mocked(buildLogoutUrl).mockResolvedValue(
      new URL(
        "http://localhost:8081/realms/truth-in-stream/protocol/openid-connect/logout?id_token_hint=id-token",
      ),
    );

    const res = await GET(new NextRequest("http://localhost:3000/auth/logout"));

    expect(vi.mocked(buildLogoutUrl)).toHaveBeenCalledWith("id-token");
    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toContain("openid-connect/logout");
    expect(store.get("kc_access")).toBeUndefined();
    expect(store.get("kc_refresh")).toBeUndefined();
    expect(store.get("kc_id")).toBeUndefined();
  });

  test("still clears the local session and lands home when the end-session URL cannot be built", async () => {
    store.set("kc_access", "a");
    vi.mocked(buildLogoutUrl).mockRejectedValue(new Error("discovery down"));

    const res = await GET(new NextRequest("http://localhost:3000/auth/logout"));

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toBe("http://localhost:3000/");
    expect(store.get("kc_access")).toBeUndefined();
  });
});
