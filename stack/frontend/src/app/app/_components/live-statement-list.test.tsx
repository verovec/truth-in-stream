import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { renderWithPlayback } from "@/test/playback";
import type { SkipReason } from "@/lib/fact-check/api";
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
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/the earth is round/i)).toBeInTheDocument();
    expect(screen.getByText(/checking this statement/i)).toBeInTheDocument();
  });

  test("labels a statement with its diarized speaker when present", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          {
            id: "0",
            start: 0,
            end: 2,
            text: "the earth is round",
            speaker: "A",
            status: "analysing",
          },
        ]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/speaker a/i)).toBeInTheDocument();
  });

  test("omits the speaker tag for an unattributed statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
      />,
    );

    expect(screen.queryByText(/speaker/i)).not.toBeInTheDocument();
  });

  test("does not render verdicts inline; those live in the fact-check region", () => {
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
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/the earth is round/i)).toBeInTheDocument();
    expect(screen.queryByText(/oblate spheroid/i)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "NASA" }),
    ).not.toBeInTheDocument();
  });

  test("surfaces a non-fatal analysis error without a verdict", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "garbled", { error: "analysis failed" })]}
        selectedStatementId={null}
      />,
    );

    expect(
      screen.getByText(/this statement could not be checked/i),
    ).toBeInTheDocument();
  });

  test("notes a skipped statement's reason rather than a verdict", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "small talk", { skipReason: "not_a_claim" })]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/no verifiable claim/i)).toBeInTheDocument();
  });

  test("reads cleanly for a skip reason the frontend does not recognise", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "mystery", {
            skipReason: "brand_new_reason" as SkipReason,
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(
      screen.getByText(/not checked - an unrecognised reason/i),
    ).toBeInTheDocument();
  });

  test("notes when a checked statement found no confident match", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "obscure aside", { matches: [] })]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/no confident match/i)).toBeInTheDocument();
  });

  test("shows the corroboration percentage for a scored statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "the earth is round", {
            matches: [
              {
                kind: "claim",
                claim: "Earth is an oblate spheroid",
                verdict: "corroborates",
                sources: [],
                similarity: 0.92,
              },
            ],
            confidence: {
              score: 0.82,
              supporting: 0.92,
              contradicting: 0,
              evidenceItems: 1,
            },
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/82%/)).toBeInTheDocument();
    expect(
      screen.getByText(/corroborated by the reference corpus/i),
    ).toBeInTheDocument();
  });

  test("shows no percentage for a skipped statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "small talk", {
            skipReason: "not_a_claim",
            confidence: undefined,
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(screen.queryByText(/corroborated by the reference corpus/i)).toBeNull();
  });

  test("marks the statement at the playback position active and seeks on click", async () => {
    const { store } = renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
        selectedStatementId={null}
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

  test("scrolls the selected statement into view", () => {
    const spy = vi
      .spyOn(HTMLElement.prototype, "scrollIntoView")
      .mockImplementation(() => {});
    try {
      renderWithPlayback(
        <LiveStatementList
          statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
          selectedStatementId="1"
        />,
      );

      const selected = screen.getByText("second").closest("li");
      expect(spy.mock.instances).toContain(selected);
    } finally {
      spy.mockRestore();
    }
  });
});
