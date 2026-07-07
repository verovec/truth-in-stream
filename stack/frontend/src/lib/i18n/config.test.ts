import { describe, expect, test } from "vitest";
import {
  defaultLocale,
  isLocale,
  locales,
  negotiate,
} from "./config";

describe("i18n config", () => {
  test("French is the default locale", () => {
    expect(defaultLocale).toBe("fr");
    expect(locales).toEqual(["fr", "en"]);
  });

  test("isLocale narrows only the known lowercase locales", () => {
    expect(isLocale("fr")).toBe(true);
    expect(isLocale("en")).toBe(true);
    expect(isLocale("de")).toBe(false);
    expect(isLocale("FR")).toBe(false);
    expect(isLocale("")).toBe(false);
    expect(isLocale("fr-FR")).toBe(false);
  });
});

describe("negotiate", () => {
  test("falls back to French when there is no Accept-Language", () => {
    expect(negotiate(null)).toBe("fr");
    expect(negotiate(undefined)).toBe("fr");
    expect(negotiate("")).toBe("fr");
  });

  test("returns English when English is preferred", () => {
    expect(negotiate("en-US,en;q=0.9")).toBe("en");
    expect(negotiate("en")).toBe("en");
    expect(negotiate("fr;q=0.5,en;q=0.9")).toBe("en");
  });

  test("returns French when French is preferred or present", () => {
    expect(negotiate("fr-FR,fr;q=0.9,en;q=0.8")).toBe("fr");
    expect(negotiate("fr")).toBe("fr");
  });

  test("French wins ties", () => {
    expect(negotiate("en;q=0.8,fr;q=0.8")).toBe("fr");
    expect(negotiate("en,fr")).toBe("fr");
  });

  test("falls back to French for unknown languages", () => {
    expect(negotiate("de,es;q=0.9")).toBe("fr");
    expect(negotiate("*")).toBe("fr");
  });

  test("treats an explicit q=0 as 'not acceptable', not a preference", () => {
    expect(negotiate("de,en;q=0")).toBe("fr");
    expect(negotiate("en;q=0,fr;q=0")).toBe("fr");
    expect(negotiate("en;q=0,fr;q=0.5")).toBe("fr");
    expect(negotiate("en;q=0.5,fr;q=0")).toBe("en");
  });

  test("parses the q parameter case-insensitively", () => {
    expect(negotiate("en;Q=0.3,fr;q=0.9")).toBe("fr");
    expect(negotiate("en;Q=0.9,fr;Q=0.2")).toBe("en");
  });
});
