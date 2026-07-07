import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { Logo } from "./logo";

describe("Logo", () => {
  test("exposes an accessible name by default", () => {
    render(<Logo />);
    expect(
      screen.getByRole("img", { name: /jeminforme\.fr/i }),
    ).toBeInTheDocument();
  });

  test("carries the tricolore in the mark", () => {
    const { container } = render(<Logo />);
    const strokes = Array.from(container.querySelectorAll("path")).map((p) =>
      p.getAttribute("stroke")?.toLowerCase(),
    );
    expect(strokes).toContain("#0055a4");
    expect(strokes).toContain("#ef4135");
  });

  test("can render as a decorative mark with no accessible name", () => {
    const { container } = render(<Logo decorative />);
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    expect(container.querySelector("svg")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
  });
});
