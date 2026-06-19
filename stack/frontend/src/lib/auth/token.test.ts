import { describe, expect, test } from "vitest";
import {
  decodeAccessToken,
  isExpired,
  roleFromClaims,
  roleFromToken,
  secondsUntilExpiry,
} from "./token";

function makeToken(payload: Record<string, unknown>): string {
  const header = base64Url(JSON.stringify({ alg: "RS256", typ: "JWT" }));
  const body = base64Url(JSON.stringify(payload));
  return `${header}.${body}.signature`;
}

function base64Url(value: string): string {
  return Buffer.from(value, "utf8")
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

describe("decodeAccessToken", () => {
  test("decodes a well-formed token payload", () => {
    const token = makeToken({ exp: 100, realm_access: { roles: ["guest"] } });

    expect(decodeAccessToken(token)).toEqual({
      exp: 100,
      realm_access: { roles: ["guest"] },
    });
  });

  test("returns null for a token without three segments", () => {
    expect(decodeAccessToken("not.a-jwt")).toBeNull();
  });

  test("returns null for a token whose payload is not JSON", () => {
    expect(decodeAccessToken("aaa.bbb.ccc")).toBeNull();
  });
});

describe("roleFromClaims / roleFromToken", () => {
  test("reports admin when the realm admin role is present", () => {
    expect(roleFromClaims({ realm_access: { roles: ["admin", "guest"] } })).toBe(
      "admin",
    );
    expect(
      roleFromToken(makeToken({ realm_access: { roles: ["admin", "guest"] } })),
    ).toBe("admin");
  });

  test("reports guest when only the guest role is present", () => {
    expect(roleFromClaims({ realm_access: { roles: ["guest"] } })).toBe("guest");
    expect(
      roleFromToken(makeToken({ realm_access: { roles: ["guest"] } })),
    ).toBe("guest");
  });

  test("reports guest for null claims, a roleless token, or a malformed token", () => {
    expect(roleFromClaims(null)).toBe("guest");
    expect(roleFromClaims({})).toBe("guest");
    expect(roleFromToken("garbage")).toBe("guest");
  });
});

describe("isExpired", () => {
  test("is false for a token whose exp is well in the future", () => {
    const token = makeToken({ exp: 1000 });

    expect(isExpired(token, 100)).toBe(false);
  });

  test("is true once now plus the buffer reaches exp", () => {
    const token = makeToken({ exp: 1000 });

    expect(isExpired(token, 980, 30)).toBe(true);
  });

  test("is true for a token with no exp or one that cannot decode", () => {
    expect(isExpired(makeToken({}), 0)).toBe(true);
    expect(isExpired("garbage", 0)).toBe(true);
  });
});

describe("secondsUntilExpiry", () => {
  test("returns the whole seconds remaining before exp", () => {
    const token = makeToken({ exp: 1000 });

    expect(secondsUntilExpiry(token, 940)).toBe(60);
  });

  test("floors at the minimum for an already-expired token so the cookie is never written already-deleted", () => {
    const token = makeToken({ exp: 1000 });

    expect(secondsUntilExpiry(token, 2000)).toBe(30);
  });

  test("returns the fallback for a token with no decodable exp", () => {
    expect(secondsUntilExpiry("garbage", 0, 45)).toBe(45);
  });
});
