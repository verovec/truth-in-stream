import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { AppHeader } from "./app-header";

describe("AppHeader", () => {
  test("links only the inactive section to its route", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="guest" />);
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
    render(
      <AppHeader
        dict={fr}
        locale="fr"
        currentSection="documents"
        role="guest"
      />,
    );
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
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="guest" />);
    expect(screen.getByText(fr.brand.name)).toBeInTheDocument();
    expect(screen.getByText(fr.app.header.signOut)).toBeInTheDocument();
  });

  test("reveals the backoffice entry only to an admin", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="admin" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    const backoffice = within(nav).getByRole("link", {
      name: fr.app.nav.backoffice,
    });
    expect(backoffice).toHaveAttribute("href", "/backoffice");
  });

  test("hides the backoffice entry from a guest", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="guest" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    expect(
      within(nav).queryByText(fr.app.nav.backoffice),
    ).not.toBeInTheDocument();
  });

  test("marks the backoffice inert when it is the current section", () => {
    render(
      <AppHeader
        dict={fr}
        locale="fr"
        currentSection="backoffice"
        role="admin"
      />,
    );
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    const current = within(nav).getByText(fr.app.nav.backoffice);
    expect(current).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).queryByRole("link", { name: fr.app.nav.backoffice }),
    ).not.toBeInTheDocument();
  });

  test("shows the TV entry to every authenticated user, linking to /tv", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="guest" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    const tv = within(nav).getByRole("link", { name: fr.app.nav.tv });
    expect(tv).toHaveAttribute("href", "/tv");
    expect(tv).not.toHaveAttribute("aria-current");
  });

  test("marks TV inert when it is the current section", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="tv" role="guest" />);
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    const current = within(nav).getByText(fr.app.nav.tv);
    expect(current).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).queryByRole("link", { name: fr.app.nav.tv }),
    ).not.toBeInTheDocument();
  });

  test("pins to the top as a sticky, translucent bar", () => {
    render(<AppHeader dict={fr} locale="fr" currentSection="videos" role="guest" />);
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
