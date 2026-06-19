import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { SessionKeepalive } from "./session-keepalive";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("SessionKeepalive", () => {
  test("posts to the refresh route on its interval", () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", fetchMock);

    render(<SessionKeepalive intervalMs={1000} />);
    expect(fetchMock).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1000);
    expect(fetchMock).toHaveBeenCalledWith("/auth/refresh", {
      method: "POST",
      keepalive: true,
    });

    vi.advanceTimersByTime(1000);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  test("clears the interval on unmount so it stops refreshing", () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true });
    vi.stubGlobal("fetch", fetchMock);

    const { unmount } = render(<SessionKeepalive intervalMs={1000} />);
    unmount();

    vi.advanceTimersByTime(5000);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  test("swallows a failed refresh without throwing", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new Error("network down")),
    );

    render(<SessionKeepalive intervalMs={1000} />);
    expect(() => vi.advanceTimersByTime(1000)).not.toThrow();
  });
});
