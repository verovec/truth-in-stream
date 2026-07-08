import { beforeEach, describe, expect, test, vi } from "vitest";
import { cookies } from "next/headers";
import { LOCALE_COOKIE } from "@/lib/i18n/request";
import { setLocalePreference } from "./set-locale";

vi.mock("next/headers", () => ({
  cookies: vi.fn(),
  headers: vi.fn(),
}));

describe("setLocalePreference", () => {
  const set = vi.fn();

  beforeEach(() => {
    set.mockReset();
    vi.mocked(cookies).mockResolvedValue({ set } as unknown as Awaited<
      ReturnType<typeof cookies>
    >);
  });

  test("persists a supported locale in the preference cookie", async () => {
    await setLocalePreference("en");
    expect(set).toHaveBeenCalledTimes(1);
    const [name, value, options] = set.mock.calls[0];
    expect(name).toBe(LOCALE_COOKIE);
    expect(value).toBe("en");
    expect(options).toMatchObject({ path: "/", sameSite: "lax" });
  });

  test("rejects an unsupported locale without touching the cookie", async () => {
    await setLocalePreference("de");
    expect(set).not.toHaveBeenCalled();
  });
});
