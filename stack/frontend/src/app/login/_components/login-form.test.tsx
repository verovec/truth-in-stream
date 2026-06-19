import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { LoginForm } from "./login-form";

describe("LoginForm", () => {
  test("renders a Keycloak sign-in link to the server-side login route", () => {
    render(<LoginForm />);

    const link = screen.getByRole("link", { name: /sign in with keycloak/i });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/auth/login");
  });

  test("surfaces an expired-session error when login could not start", () => {
    render(<LoginForm error="session" />);

    expect(
      screen.getByText(/sign-in session expired/i),
    ).toBeInTheDocument();
  });

  test("surfaces a generic error when the token exchange failed", () => {
    render(<LoginForm error="exchange" />);

    expect(
      screen.getByText(/could not be completed/i),
    ).toBeInTheDocument();
  });

  test("shows no error banner on a clean load", () => {
    render(<LoginForm />);

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
