import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";

import { DebugSurface } from "./debug-surface";

// Minimal WebSocket stand-in so revealing the wiki-search panel does not attempt
// a real connection.
class StubWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  binaryType = "blob";
  readyState = StubWebSocket.CONNECTING;
  addEventListener() {}
  send() {}
  close() {}
}

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("DebugSurface", () => {
  test("renders nothing for a guest", () => {
    const { container } = render(<DebugSurface role="guest" />);

    expect(container).toBeEmptyDOMElement();
  });

  test("renders the debug toggle for an admin", () => {
    render(<DebugSurface role="admin" />);

    expect(screen.getByRole("button", { name: /debug/i })).toBeInTheDocument();
  });

  test("does not reveal the probe until the admin opens it, then reveals it", async () => {
    vi.stubGlobal("WebSocket", StubWebSocket);
    const user = userEvent.setup();
    render(<DebugSurface role="admin" />);

    expect(
      screen.queryByRole("searchbox", { name: /wiki search query/i }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /debug/i }));

    expect(
      screen.getByRole("searchbox", { name: /wiki search query/i }),
    ).toBeInTheDocument();
  });
});
