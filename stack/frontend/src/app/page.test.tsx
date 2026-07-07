import { describe, expect, test, vi } from "vitest";

const redirect = vi.fn();
const getHeader = vi.fn();

vi.mock("next/navigation", () => ({
  redirect: (...args: unknown[]) => redirect(...args),
}));
vi.mock("next/headers", () => ({
  headers: async () => ({ get: getHeader }),
}));

import RootPage from "./page";

describe("root locale redirect", () => {
  test("redirects to French by default", async () => {
    getHeader.mockReturnValue(null);
    await RootPage();
    expect(redirect).toHaveBeenCalledWith("/fr");
  });

  test("redirects to English when English is preferred", async () => {
    getHeader.mockReturnValue("en-US,en;q=0.9,fr;q=0.5");
    await RootPage();
    expect(redirect).toHaveBeenCalledWith("/en");
  });

  test("reads the Accept-Language header to negotiate", async () => {
    getHeader.mockReturnValue("fr-FR,fr;q=0.9");
    await RootPage();
    expect(getHeader).toHaveBeenCalledWith("accept-language");
    expect(redirect).toHaveBeenCalledWith("/fr");
  });
});
