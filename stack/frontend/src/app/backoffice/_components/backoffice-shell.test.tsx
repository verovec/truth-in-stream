import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { en } from "@/lib/i18n/dictionaries/en";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { BackofficeShell } from "./backoffice-shell";

describe("BackofficeShell", () => {
  // The shell mounts SessionKeepalive when authenticated, which fetches; stub the
  // network so the test exercises the shell markup, not the backend.
  beforeEach(() => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({}), { status: 200 }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  test("marks Backoffice current, keeps the other areas linked, and renders both sections", () => {
    render(<BackofficeShell role="admin" authenticated locale="fr" dict={fr} />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    // Backoffice is the current section: inert (aria-current, not a link).
    expect(within(nav).getByText(fr.app.nav.backoffice)).toHaveAttribute(
      "aria-current",
      "page",
    );
    // The consumption areas stay navigable links.
    expect(
      within(nav).getByRole("link", { name: fr.app.nav.videos }),
    ).toHaveAttribute("href", "/app");
    expect(
      within(nav).getByRole("link", { name: fr.app.nav.documents }),
    ).toHaveAttribute("href", "/documents");
    // The area title and both section headings render under the header.
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: fr.app.backoffice.heading,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", {
        level: 3,
        name: fr.app.backoffice.videos.heading,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", {
        level: 3,
        name: fr.app.backoffice.documents.heading,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(fr.app.backoffice.intro)).toBeInTheDocument();
  });

  test("labels the content subtree with the active locale and renders English copy", () => {
    const { container } = render(
      <BackofficeShell role="admin" authenticated locale="en" dict={en} />,
    );
    expect(container.querySelector('[lang="en"]')).not.toBeNull();
    expect(
      screen.getByRole("heading", {
        level: 2,
        name: en.app.backoffice.heading,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText(en.app.backoffice.intro)).toBeInTheDocument();
  });
});
