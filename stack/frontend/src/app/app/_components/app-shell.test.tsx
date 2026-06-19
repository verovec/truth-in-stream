import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { json, stubBackend } from "@/test/fact-check";
import { AppShell } from "./app-shell";

vi.mock("react-player", () => import("@/test/react-player-mock"));

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

class StubWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  binaryType = "blob";
  readyState = StubWebSocket.CONNECTING;
  addEventListener() {}
  send() {}
  close() {}
}

function stubVideos() {
  stubBackend([
    {
      match: (url, init) =>
        url.endsWith("/api/videos") && (init?.method ?? "GET") === "GET",
      responses: [json(200, { videos: [] })],
    },
  ]);
}

describe("AppShell", () => {
  test("renders the header and the video library", async () => {
    stubVideos();

    render(<AppShell role="guest" authenticated={false} />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Truth in Stream" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /sign out/i }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });
  });

  test("hides the debug surface from a guest", async () => {
    stubVideos();

    render(<AppShell role="guest" authenticated={false} />);

    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: /^debug$/i }),
    ).not.toBeInTheDocument();
  });

  test("reveals the admin debug surface", async () => {
    vi.stubGlobal("WebSocket", StubWebSocket);
    stubVideos();

    render(<AppShell role="admin" authenticated={true} />);

    await waitFor(() => {
      expect(screen.getByText(/no videos yet/i)).toBeInTheDocument();
    });
    expect(
      screen.getByRole("button", { name: /^debug$/i }),
    ).toBeInTheDocument();
  });
});
