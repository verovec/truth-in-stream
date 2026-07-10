import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { DocumentsShell } from "./documents-shell";

describe("DocumentsShell", () => {
  // The shell mounts SessionKeepalive and the experience, both of which fetch;
  // stub the network so the test exercises the shell markup, not the backend.
  beforeEach(() => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ documents: [] }), { status: 200 }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  test("renders the shared header with Documents marked as the current section", () => {
    render(
      <DocumentsShell role="guest" authenticated locale="fr" dict={fr} />,
    );
    const nav = screen.getByRole("navigation", { name: fr.app.nav.ariaLabel });
    // The current section is inert (aria-current, not a link) so it cannot
    // navigate; the Videos section is the link.
    expect(
      within(nav).getByText(fr.app.nav.documents),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: fr.app.nav.videos }),
    ).toHaveAttribute("href", "/app");
    // The documents area heading renders under the header.
    expect(
      screen.getByRole("heading", { name: fr.app.documents.heading }),
    ).toBeInTheDocument();
  });

  test("a guest sees no admin upload zone", () => {
    render(
      <DocumentsShell role="guest" authenticated locale="fr" dict={fr} />,
    );
    expect(screen.queryByTestId("document-upload-zone")).not.toBeInTheDocument();
  });
});
