import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { AppHeader } from "./app-header";

describe("AppHeader", () => {
  test("links only the inactive section to its route", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    // The other section is a real navigating link.
    const documents = within(nav).getByRole("link", { name: fr.app.nav.documents });
    expect(documents).toHaveAttribute("href", "/documents");
    // The current section is not a link at all, so it cannot navigate.
    expect(
      within(nav).queryByRole("link", { name: fr.app.nav.videos }),
    ).not.toBeInTheDocument();
  });

  test("marks the current section inert with aria-current and never links to itself", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="documents" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    // The current section: present, aria-current, but not a link (a click on it
    // must not hard-navigate and tear down a live-analysis session).
    const current = within(nav).getByText(fr.app.nav.documents);
    expect(current).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).queryByRole("link", { name: fr.app.nav.documents }),
    ).not.toBeInTheDocument();
    // The inactive section stays a link with no aria-current.
    const videos = within(nav).getByRole("link", { name: fr.app.nav.videos });
    expect(videos).toHaveAttribute("href", "/app");
    expect(videos).not.toHaveAttribute("aria-current");
  });

  test("shows the brand wordmark and the sign-out control", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" />);
    expect(screen.getByText(fr.brand.name)).toBeInTheDocument();
    expect(screen.getByText(fr.app.header.signOut)).toBeInTheDocument();
  });

  test("pins to the top as a sticky, translucent bar", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" />);
    // The header stays visible while the operator scrolls the analysis view, so
    // the navigation and brand remain reachable. It is a translucent, blurred bar
    // layered above the scrolling content.
    const header = screen.getByRole("banner");
    expect(header.className).toContain("sticky");
    expect(header.className).toContain("top-0");
    expect(header.className).toContain("z-40");
    expect(header.className).toContain("backdrop-blur");
  });
});
