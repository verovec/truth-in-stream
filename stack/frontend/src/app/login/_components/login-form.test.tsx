import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { LoginForm } from "./login-form";

describe("LoginForm", () => {
  test("renders a Keycloak sign-in link to the server-side login route", () => {
    render(<LoginForm copy={fr.login} />);

    const link = screen.getByRole("link", { name: fr.login.signIn });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/auth/login");
  });

  test("surfaces an expired-session error when login could not start", () => {
    render(<LoginForm error="session" copy={fr.login} />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      fr.login.errors.session,
    );
  });

  test("surfaces a generic error when the token exchange failed", () => {
    render(<LoginForm error="exchange" copy={fr.login} />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      fr.login.errors.exchange,
    );
  });

  test("shows no error banner on a clean load", () => {
    render(<LoginForm copy={fr.login} />);

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
