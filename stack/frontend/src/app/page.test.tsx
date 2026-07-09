import { describe, expect, test, vi } from "vitest";

const redirect = vi.fn();
const getHeader = vi.fn();
const getCookie = vi.fn();

vi.mock("next/navigation", () => ({
  redirect: (...args: unknown[]) => redirect(...args),
}));
vi.mock("next/headers", () => ({
  headers: async () => ({ get: getHeader }),
  cookies: async () => ({ get: getCookie }),
}));

import RootPage from "./page";

describe("root locale redirect", () => {
  test("redirects to French by default", async () => {
    getCookie.mockReturnValue(undefined);
    getHeader.mockReturnValue(null);
    await RootPage();
    expect(redirect).toHaveBeenCalledWith("/fr");
  });

  test("redirects to English when English is preferred", async () => {
    getCookie.mockReturnValue(undefined);
    getHeader.mockReturnValue("en-US,en;q=0.9,fr;q=0.5");
    await RootPage();
    expect(redirect).toHaveBeenCalledWith("/en");
  });

  test("reads the Accept-Language header to negotiate", async () => {
    getCookie.mockReturnValue(undefined);
    getHeader.mockReturnValue("fr-FR,fr;q=0.9");
    await RootPage();
    expect(getHeader).toHaveBeenCalledWith("accept-language");
    expect(redirect).toHaveBeenCalledWith("/fr");
  });

  test("honours an in-app locale preference cookie over Accept-Language", async () => {
    // The cookie is written only by the in-app FR/EN toggle; an English choice
    // must survive a visit to the bare root even from a French browser.
    getCookie.mockReturnValue({ name: "locale", value: "en" });
    getHeader.mockReturnValue("fr-FR,fr;q=0.9");
    await RootPage();
    expect(redirect).toHaveBeenCalledWith("/en");
  });
});
