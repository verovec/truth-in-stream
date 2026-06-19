// @vitest-environment node
import { describe, expect, test } from "vitest";
import * as oauth from "oauth4webapi";
import { insecureOption } from "./oidc";

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
