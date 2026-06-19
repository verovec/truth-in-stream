import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { LogoutButton } from "./logout-button";

describe("LogoutButton", () => {
  test("links to the server-side logout route that ends the Keycloak session", () => {
    render(<LogoutButton />);

    const link = screen.getByRole("link", { name: /sign out/i });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/auth/logout");
  });
});
