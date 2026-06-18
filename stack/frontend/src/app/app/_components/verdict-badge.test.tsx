import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import type { LiteralVerdict, ManipulationFlag } from "@/lib/live/frames";
import { FlagChips, LiteralBadge, VerdictBadge } from "./verdict-badge";

describe("VerdictBadge", () => {
  test("renders the curated verdict label", () => {
    render(<VerdictBadge verdict="corroborates" />);
    expect(screen.getByText("corroborates")).toBeInTheDocument();
  });
});

describe("LiteralBadge", () => {
  const cases: [LiteralVerdict, string][] = [
    ["accurate", "Exact"],
    ["inaccurate", "Inexact"],
    ["unverifiable", "Invérifiable"],
  ];

  test.each(cases)(
    "renders the French label for the %s literal verdict",
    (literal, label) => {
      render(<LiteralBadge literal={literal} />);
      expect(screen.getByText(label)).toBeInTheDocument();
    },
  );
});

describe("FlagChips", () => {
  const flags: [ManipulationFlag, string][] = [
    ["missing-context", "Contexte manquant"],
    ["cherry-picked", "Données triées"],
    ["outdated", "Périmé"],
    ["misattributed", "Mal attribué"],
    ["misleading-causation", "Causalité trompeuse"],
  ];

  test.each(flags)("renders a French chip for the %s flag", (flag, label) => {
    render(<FlagChips flags={[flag]} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  test("renders nothing for an empty flag list", () => {
    const { container } = render(<FlagChips flags={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  test("renders every flag in a multi-flag claim", () => {
    render(<FlagChips flags={["cherry-picked", "outdated"]} />);
    expect(screen.getByText("Données triées")).toBeInTheDocument();
    expect(screen.getByText("Périmé")).toBeInTheDocument();
  });
});
