import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { en } from "@/lib/i18n/dictionaries/en";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { json, stubBackend } from "@/test/fact-check";
import { AppShell } from "./app-shell";

vi.mock("react-player", () => import("@/test/react-player-mock"));

vi.mock("next/navigation", () => import("@/test/next-navigation-mock"));

vi.mock("next/link", () => import("@/test/next-link-mock"));

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
  test("renders the jeminforme.fr brand, French chrome, and the library", async () => {
    stubVideos();

    render(
      <AppShell role="guest" authenticated={false} locale="fr" dict={fr} />,
    );

    // The brand is the page's level-1 heading (an inert lockup, not a link, so a
    // stray click can't tear down a live-analysis session by navigating away).
    expect(
      screen.getByRole("heading", { level: 1, name: "jeminforme.fr" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "jeminforme.fr" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/truth in stream/i)).not.toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: fr.app.header.signOut }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: fr.langSwitch.toEnglish }),
    ).toBeInTheDocument();
    expect(screen.getByRole("main")).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText(fr.app.library.empty)).toBeInTheDocument();
    });
  });

  test("labels the content subtree with the active locale", async () => {
    stubVideos();

    const { container } = render(
      <AppShell role="guest" authenticated={false} locale="en" dict={en} />,
    );

    expect(container.querySelector('[lang="en"]')).not.toBeNull();
    expect(
      screen.getByRole("link", { name: en.app.header.signOut }),
    ).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText(en.app.library.empty)).toBeInTheDocument();
    });
  });

  test("hides the debug surface from a guest", async () => {
    stubVideos();

    render(
      <AppShell role="guest" authenticated={false} locale="fr" dict={fr} />,
    );

    await waitFor(() => {
      expect(screen.getByText(fr.app.library.empty)).toBeInTheDocument();
    });
    expect(
      screen.queryByRole("button", { name: /^debug$/i }),
    ).not.toBeInTheDocument();
  });

  test("reveals the admin debug surface", async () => {
    vi.stubGlobal("WebSocket", StubWebSocket);
    stubVideos();

    render(
      <AppShell role="admin" authenticated={true} locale="fr" dict={fr} />,
    );

    await waitFor(() => {
      expect(screen.getByText(fr.app.library.empty)).toBeInTheDocument();
    });
    expect(
      screen.getByRole("button", { name: /^debug$/i }),
    ).toBeInTheDocument();
  });
});
