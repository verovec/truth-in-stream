import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { LogoutButton } from "./logout-button";

describe("LogoutButton", () => {
  test("links to the server-side logout route that ends the Keycloak session", () => {
    render(<LogoutButton label={fr.app.header.signOut} />);

    const link = screen.getByRole("link", { name: fr.app.header.signOut });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/auth/logout");
  });
});
