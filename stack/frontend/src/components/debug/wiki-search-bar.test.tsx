import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { WikiSearchBar } from "./wiki-search-bar";

// Minimal WebSocket stand-in so mounting the panel does not attempt a real
// connection. createLiveSocket only sets binaryType, registers listeners, and
// may call close(); none of that needs a live socket here.
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

describe("WikiSearchBar", () => {
  test("renders nothing in a production build", () => {
    vi.stubEnv("NODE_ENV", "production");
    const { container } = render(<WikiSearchBar />);
    expect(container).toBeEmptyDOMElement();
  });

  test("renders the search input outside production", () => {
    vi.stubGlobal("WebSocket", StubWebSocket);
    render(<WikiSearchBar />);
    expect(
      screen.getByRole("searchbox", { name: /wiki search query/i }),
    ).toBeInTheDocument();
    expect(screen.getByText(/embedded wiki search/i)).toBeInTheDocument();
  });
});
