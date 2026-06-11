import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { push, refresh } from "@/test/next-navigation-mock";
import { LogoutButton } from "./logout-button";

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

beforeEach(() => {
  push.mockClear();
  refresh.mockClear();
});

describe("LogoutButton", () => {
  test("posts logout and returns to the public landing page", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(<LogoutButton />);

    await user.click(screen.getByRole("button", { name: /sign out/i }));

    expect(fetchMock).toHaveBeenCalledWith("/api/logout", { method: "POST" });
    // Not "/login": that route is intercepted by the login modal. The landing
    // page is the signed-out destination.
    expect(push).toHaveBeenCalledWith("/");
    expect(refresh).toHaveBeenCalled();
  });

  test("stays put and says so when the logout request fails, so the session state stays honest", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("network down")));
    const user = userEvent.setup();
    render(<LogoutButton />);

    await user.click(screen.getByRole("button", { name: /sign out/i }));

    expect(
      await screen.findByText("Sign out failed. Try again."),
    ).toBeInTheDocument();
    expect(push).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
  });

  test("stays put and says so on a non-ok logout response", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 500 }));
    const user = userEvent.setup();
    render(<LogoutButton />);

    await user.click(screen.getByRole("button", { name: /sign out/i }));

    expect(
      await screen.findByText("Sign out failed. Try again."),
    ).toBeInTheDocument();
    expect(push).not.toHaveBeenCalled();
  });
});
