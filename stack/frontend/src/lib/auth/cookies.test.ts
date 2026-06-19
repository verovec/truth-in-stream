import { describe, expect, test } from "vitest";
import {
  cookieSecure,
  sessionCookieOptions,
  transactionCookieOptions,
  TRANSACTION_MAX_AGE,
} from "./cookies";

describe("sessionCookieOptions", () => {
  test("is httpOnly, lax, site-wide, and carries the given lifetime", () => {
    expect(sessionCookieOptions(true, 300)).toEqual({
      httpOnly: true,
      secure: true,
      sameSite: "lax",
      path: "/",
      maxAge: 300,
    });
  });

  test("honors the secure flag", () => {
    expect(sessionCookieOptions(false, 300).secure).toBe(false);
  });
});

describe("transactionCookieOptions", () => {
  test("uses the bounded transaction lifetime", () => {
    expect(transactionCookieOptions(true).maxAge).toBe(TRANSACTION_MAX_AGE);
    expect(transactionCookieOptions(true).httpOnly).toBe(true);
  });
});

describe("cookieSecure", () => {
  test("is secure in production", () => {
    expect(cookieSecure({ NODE_ENV: "production" })).toBe(true);
  });

  test("is insecure outside production", () => {
    expect(cookieSecure({ NODE_ENV: "development" })).toBe(false);
  });

  test("opts out of secure when AUTH_INSECURE_COOKIE is set, even in production", () => {
    expect(
      cookieSecure({ NODE_ENV: "production", AUTH_INSECURE_COOKIE: "true" }),
    ).toBe(false);
  });
});
