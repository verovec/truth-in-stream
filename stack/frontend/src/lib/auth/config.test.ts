// @vitest-environment node
import { describe, expect, test, vi } from "vitest";

vi.mock("server-only", () => ({}));

import { readOidcConfig } from "./config";

describe("readOidcConfig", () => {
  test("defaults to the local-dev realm and derives the redirect URIs", () => {
    const cfg = readOidcConfig({});

    expect(cfg.issuer).toBe("http://localhost:8081/realms/truth-in-stream");
    expect(cfg.clientId).toBe("truth-in-stream-web");
    expect(cfg.appUrl).toBe("http://localhost:3000");
    expect(cfg.redirectUri).toBe("http://localhost:3000/auth/callback");
    expect(cfg.postLogoutRedirectUri).toBe("http://localhost:3000/");
  });

  test("internalUrl defaults to the issuer (single-host topology)", () => {
    expect(readOidcConfig({}).internalUrl).toBe(
      "http://localhost:8081/realms/truth-in-stream",
    );
    expect(
      readOidcConfig({ KEYCLOAK_ISSUER: "https://id.example.com/realms/prod" })
        .internalUrl,
    ).toBe("https://id.example.com/realms/prod");
  });

  test("KEYCLOAK_INTERNAL_URL overrides the discovery host and trims slashes", () => {
    const cfg = readOidcConfig({
      KEYCLOAK_ISSUER: "http://localhost:8081/realms/truth-in-stream",
      KEYCLOAK_INTERNAL_URL: "http://keycloak:8081/realms/truth-in-stream/",
    });

    expect(cfg.issuer).toBe("http://localhost:8081/realms/truth-in-stream");
    expect(cfg.internalUrl).toBe("http://keycloak:8081/realms/truth-in-stream");
  });

  test("overrides from the environment and trims trailing slashes", () => {
    const cfg = readOidcConfig({
      KEYCLOAK_ISSUER: "https://id.example.com/realms/prod/",
      KEYCLOAK_CLIENT_ID: "web",
      NEXT_PUBLIC_APP_URL: "https://app.example.com/",
    });

    expect(cfg.issuer).toBe("https://id.example.com/realms/prod");
    expect(cfg.clientId).toBe("web");
    expect(cfg.appUrl).toBe("https://app.example.com");
    expect(cfg.redirectUri).toBe("https://app.example.com/auth/callback");
    expect(cfg.postLogoutRedirectUri).toBe("https://app.example.com/");
  });
});
