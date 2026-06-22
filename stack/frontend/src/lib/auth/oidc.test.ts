// @vitest-environment node
import { afterEach, describe, expect, test, vi } from "vitest";
import * as oauth from "oauth4webapi";

vi.mock("server-only", () => ({}));
// Keep the real module (insecureOption depends on the allowInsecureRequests
// symbol) but stub the two network round trips so authServer can be exercised
// without a Keycloak.
vi.mock("oauth4webapi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("oauth4webapi")>();
  return { ...actual, discoveryRequest: vi.fn(), processDiscoveryResponse: vi.fn() };
});

import { authServer, insecureOption } from "./oidc";

describe("insecureOption", () => {
  test("allows plain HTTP for a local-dev http issuer", () => {
    expect(insecureOption("http://localhost:8081/realms/truth-in-stream")).toEqual(
      { [oauth.allowInsecureRequests]: true },
    );
  });

  test("never relaxes HTTPS for a deployed https issuer", () => {
    expect(insecureOption("https://login.jeminforme.fr/realms/prod")).toEqual({});
  });
});

describe("authServer", () => {
  afterEach(() => {
    delete process.env.KEYCLOAK_ISSUER;
    delete process.env.KEYCLOAK_INTERNAL_URL;
    vi.clearAllMocks();
  });

  test("discovers over the internal URL but validates the public issuer", async () => {
    // Unique issuer so the module-level discovery cache cannot collide with
    // another test's entry.
    process.env.KEYCLOAK_ISSUER = "http://localhost:8081/realms/split-horizon";
    process.env.KEYCLOAK_INTERNAL_URL = "http://keycloak:8081/realms/split-horizon";
    const response = new Response("{}");
    const metadata = {
      issuer: process.env.KEYCLOAK_ISSUER,
    } as oauth.AuthorizationServer;
    vi.mocked(oauth.discoveryRequest).mockResolvedValue(response);
    vi.mocked(oauth.processDiscoveryResponse).mockResolvedValue(metadata);

    const result = await authServer();

    expect(result).toBe(metadata);
    // Discovery is fetched from the internal (container-reachable) host, with the
    // plain-HTTP allowance set for the http internal URL.
    const [discoveryUrl, options] = vi.mocked(oauth.discoveryRequest).mock
      .calls[0];
    expect((discoveryUrl as URL).toString()).toBe(
      "http://keycloak:8081/realms/split-horizon",
    );
    expect(options).toMatchObject({
      algorithm: "oidc",
      [oauth.allowInsecureRequests]: true,
    });
    // The response is validated against the public issuer, not the fetch host.
    const [expectedIssuer, passedResponse] = vi.mocked(
      oauth.processDiscoveryResponse,
    ).mock.calls[0];
    expect((expectedIssuer as URL).toString()).toBe(
      "http://localhost:8081/realms/split-horizon",
    );
    expect(passedResponse).toBe(response);
  });
});
