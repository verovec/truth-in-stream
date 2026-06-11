import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { renderWithPlayback } from "@/test/playback";
import type { LiveStatement } from "@/lib/live/statements";
import { LiveStatementList } from "./live-statement-list";

const analysing = (
  id: string,
  start: number,
  text: string,
): LiveStatement => ({ id, start, end: start + 2, text, status: "analysing" });

const checked = (
  id: string,
  start: number,
  text: string,
  overrides: Partial<Extract<LiveStatement, { status: "checked" }>> = {},
): LiveStatement => ({
  id,
  start,
  end: start + 2,
  text,
  status: "checked",
  matches: [],
  ...overrides,
});

describe("LiveStatementList", () => {
  test("shows an in-flight affordance for an analysing statement", () => {
    renderWithPlayback(
      <LiveStatementList statements={[analysing("0", 0, "the earth is round")]} />,
    );

    expect(screen.getByText(/the earth is round/i)).toBeInTheDocument();
    expect(screen.getByText(/checking this statement/i)).toBeInTheDocument();
  });

  test("renders a resolved verdict with its claim and sources", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "the earth is round", {
            matches: [
              {
                kind: "claim",
                claim: "Earth is an oblate spheroid",
                verdict: "corroborates",
                sources: [{ title: "NASA", url: "https://nasa.gov" }],
                similarity: 0.92,
              },
            ],
          }),
        ]}
      />,
    );

    expect(screen.getByText(/oblate spheroid/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "NASA" })).toHaveAttribute(
      "href",
      "https://nasa.gov",
    );
  });

  test("surfaces a non-fatal analysis error without a verdict", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "garbled", { error: "analysis failed" })]}
      />,
    );

    expect(
      screen.getByText(/this statement could not be checked/i),
    ).toBeInTheDocument();
  });

  test("marks the statement at the playback position active and seeks on click", async () => {
    const { store } = renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
      />,
    );

    act(() => store.update({ currentTime: 11 }));

    const active = screen.getByText("second").closest("li");
    expect(active).toHaveAttribute("aria-current", "true");

    const seek = vi.fn();
    store.registerSeekHandler(seek);
    await userEvent.click(screen.getByText("first"));
    expect(seek).toHaveBeenCalledWith(0);
  });
});
