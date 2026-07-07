import { render, screen } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";
import { CtaLink } from "./cta-link";

vi.mock("next/link", () => import("@/test/next-link-mock"));

describe("CtaLink", () => {
  test("renders a link to the given href with its label", () => {
    render(<CtaLink href="/login">Ouvrir l&apos;application</CtaLink>);
    const link = screen.getByRole("link", { name: /ouvrir l'application/i });
    expect(link).toHaveAttribute("href", "/login");
  });

  test("merges an extra className", () => {
    render(
      <CtaLink href="/login" className="mt-8">
        Go
      </CtaLink>,
    );
    expect(screen.getByRole("link", { name: "Go" })).toHaveClass("mt-8");
  });
});
