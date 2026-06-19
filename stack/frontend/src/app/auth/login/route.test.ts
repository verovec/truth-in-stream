// @vitest-environment node
import { beforeEach, describe, expect, test, vi } from "vitest";

const store = createCookieStore();
vi.mock("next/headers", () => ({ cookies: () => Promise.resolve(store) }));
vi.mock("@/lib/auth/oidc", () => ({ buildAuthorizationRequest: vi.fn() }));

import { buildAuthorizationRequest } from "@/lib/auth/oidc";
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

describe("auth login", () => {
  test("stashes the PKCE verifier and state, then redirects to Keycloak authorize", async () => {
    vi.mocked(buildAuthorizationRequest).mockResolvedValue({
      url: new URL(
        "http://localhost:8081/realms/truth-in-stream/protocol/openid-connect/auth?client_id=truth-in-stream-web",
      ),
      codeVerifier: "the-verifier",
      state: "the-state",
    });

    const res = await GET();

    expect(res.status).toBe(307);
    expect(res.headers.get("location")).toContain("openid-connect/auth");
    expect(store.get("kc_pkce_verifier")?.value).toBe("the-verifier");
    expect(store.get("kc_oauth_state")?.value).toBe("the-state");
  });
});
