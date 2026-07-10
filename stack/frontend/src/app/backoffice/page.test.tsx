import { beforeEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";

// The page's own logic is the access decision: resolve the session, then redirect
// a non-admin before the backoffice tree is built. Mock the session source and
// the locale/dictionary reads (so no server-only module loads) and assert the
// redirect matrix directly on the async page function - an async Server Component
// cannot be rendered by the test runner.
const redirect = vi.fn();
const getSession = vi.fn();

vi.mock("next/navigation", () => ({
  redirect: (...args: unknown[]) => redirect(...args),
}));
vi.mock("@/lib/auth/session", () => ({ getSession: () => getSession() }));
vi.mock("@/lib/i18n/request", () => ({
  resolveRequestLocale: async () => "fr",
}));
vi.mock("@/lib/i18n/dictionaries", () => ({ getDictionary: async () => fr }));

import BackofficePage from "./page";

describe("backoffice page access gate", () => {
  beforeEach(() => {
    redirect.mockClear();
    getSession.mockClear();
  });

  test("renders the backoffice for an admin without redirecting", async () => {
    getSession.mockResolvedValue({ role: "admin", authenticated: true });
    const element = await BackofficePage();
    expect(redirect).not.toHaveBeenCalled();
    expect(element).toBeTruthy();
  });

  test("redirects a guest to /app", async () => {
    getSession.mockResolvedValue({ role: "guest", authenticated: true });
    await BackofficePage();
    expect(redirect).toHaveBeenCalledWith("/app");
  });

  test("redirects an unauthenticated caller (resolved to guest) to /app", async () => {
    // A missing or expired cookie resolves to GUEST_SESSION (authenticated
    // false, role guest); the gate branches on role, so it bounces just the same.
    getSession.mockResolvedValue({ role: "guest", authenticated: false });
    await BackofficePage();
    expect(redirect).toHaveBeenCalledWith("/app");
  });
});
