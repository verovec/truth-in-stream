import { beforeEach, describe, expect, test, vi } from "vitest";
import { cookies, headers } from "next/headers";
import { LOCALE_COOKIE, resolveRequestLocale } from "./request";

vi.mock("next/headers", () => ({
  cookies: vi.fn(),
  headers: vi.fn(),
}));

function stubRequest({
  cookie,
  acceptLanguage,
}: {
  cookie?: string;
  acceptLanguage?: string;
}) {
  vi.mocked(cookies).mockResolvedValue({
    get: (name: string) =>
      name === LOCALE_COOKIE && cookie !== undefined
        ? { name, value: cookie }
        : undefined,
  } as Awaited<ReturnType<typeof cookies>>);
  vi.mocked(headers).mockResolvedValue(
    new Headers(
      acceptLanguage !== undefined
        ? { "accept-language": acceptLanguage }
        : {},
    ) as Awaited<ReturnType<typeof headers>>,
  );
}

describe("resolveRequestLocale", () => {
  beforeEach(() => {
    vi.mocked(cookies).mockReset();
    vi.mocked(headers).mockReset();
  });

  test("the preference cookie wins over Accept-Language", async () => {
    stubRequest({ cookie: "en", acceptLanguage: "fr-FR,fr;q=0.9" });
    await expect(resolveRequestLocale()).resolves.toBe("en");
  });

  test("an unknown cookie value falls back to negotiation", async () => {
    stubRequest({ cookie: "de", acceptLanguage: "en-GB,en;q=0.9" });
    await expect(resolveRequestLocale()).resolves.toBe("en");
  });

  test("no cookie negotiates from Accept-Language", async () => {
    stubRequest({ acceptLanguage: "en-US,en;q=0.9,fr;q=0.5" });
    await expect(resolveRequestLocale()).resolves.toBe("en");
  });

  test("nothing at all resolves to French", async () => {
    stubRequest({});
    await expect(resolveRequestLocale()).resolves.toBe("fr");
  });

  test("an ambiguous header resolves to French", async () => {
    stubRequest({ acceptLanguage: "fr;q=0.8,en;q=0.8" });
    await expect(resolveRequestLocale()).resolves.toBe("fr");
  });
});
