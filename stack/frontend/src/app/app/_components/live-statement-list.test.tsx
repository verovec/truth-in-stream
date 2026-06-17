import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { renderWithPlayback } from "@/test/playback";
import type { SkipReason } from "@/lib/fact-check/api";
import type { LiveStatement } from "@/lib/live/statements";
import { stubScrollLayout } from "@/test/scroll-layout";
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

function subtitleList() {
  return screen.getByRole("list", { name: "Subtitle transcript" });
}

describe("LiveStatementList", () => {
  test("renders a statement's atomic claims under it, suppressing the generic marker", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the bridge opened in 1937")]}
        selectedStatementId={null}
        claimsFor={(id) =>
          id === "0"
            ? [
                {
                  claimId: "0-0",
                  text: "the bridge opened in 1937",
                  status: "verified",
                  source: "verified",
                  verdict: "supports",
                },
              ]
            : []
        }
      />,
    );

    expect(screen.getByText(/supported/i)).toBeInTheDocument();
    expect(screen.getByText(/verified against evidence/i)).toBeInTheDocument();
    // The per-statement "Checking this statement" marker yields to the claim list.
    expect(
      screen.queryByText(/checking this statement/i),
    ).not.toBeInTheDocument();
  });

  test("a legacy statement with no claims renders the generic marker as before", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
        claimsFor={() => []}
      />,
    );
    expect(screen.getByText(/checking this statement/i)).toBeInTheDocument();
  });

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

  test("breaks the score down into its supporting and contradicting evidence weights", () => {
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
              score: 0.75,
              supporting: 0.9,
              contradicting: 0.3,
              evidenceItems: 2,
            },
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/2 matches/i)).toBeInTheDocument();
    expect(screen.getByText(/0\.90 supporting/i)).toBeInTheDocument();
    expect(screen.getByText(/0\.30 contradicting/i)).toBeInTheDocument();
  });

  test("reads the breakdown's evidence count in the singular for a lone match", () => {
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
              score: 1,
              supporting: 0.92,
              contradicting: 0,
              evidenceItems: 1,
            },
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/1 match\b/i)).toBeInTheDocument();
  });

  test("shows no breakdown for a skipped statement", () => {
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

    expect(screen.queryByText(/supporting/i)).toBeNull();
    expect(screen.queryByText(/contradicting/i)).toBeNull();
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

  test("renders an inconsistency flag linking back to the earlier statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("1", 10, "the bridge opened in 1940", {
            speaker: "A",
            inconsistency: {
              earlierId: "0",
              earlierText: "the bridge opened in 1937",
              rationale: "1937 versus 1940 for the same event",
            },
          }),
        ]}
        selectedStatementId={null}
      />,
    );

    expect(
      screen.getByText(/contradicts an earlier statement/i),
    ).toBeInTheDocument();
    // The earlier statement is a navigable link, not just quoted text.
    expect(
      screen.getByRole("button", { name: /the bridge opened in 1937/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/1937 versus 1940 for the same event/i),
    ).toBeInTheDocument();
  });

  test("the inconsistency link scrolls the earlier statement into view within the list, not the page", async () => {
    const { scrollTo, scrollIntoView, restore } = stubScrollLayout();
    try {
      renderWithPlayback(
        <LiveStatementList
          statements={[
            checked("0", 0, "an earlier remark about the bridge", {
              speaker: "A",
            }),
            checked("1", 10, "the bridge opened in 1940", {
              speaker: "A",
              inconsistency: {
                earlierId: "0",
                earlierText: "the bridge opened in 1937",
              },
            }),
          ]}
          selectedStatementId={null}
        />,
      );
      scrollTo.mockClear();

      await userEvent.click(
        screen.getByRole("button", { name: /the bridge opened in 1937/i }),
      );
      // The reveal scrolls the subtitle list itself, never the page.
      expect(scrollTo.mock.instances).toContain(subtitleList());
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("renders no inconsistency flag when a statement is internally consistent", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "the earth is round")]}
        selectedStatementId={null}
      />,
    );

    expect(
      screen.queryByText(/contradicts an earlier statement/i),
    ).not.toBeInTheDocument();
  });

  test("renders the newest statement at the top, older ones below", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "the oldest remark"),
          checked("1", 10, "a middle remark"),
          checked("2", 20, "the newest remark"),
        ]}
        selectedStatementId={null}
      />,
    );

    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("the newest remark");
    expect(rows[1]).toHaveTextContent("a middle remark");
    expect(rows[2]).toHaveTextContent("the oldest remark");
  });

  test("auto-reveals a newly arrived statement by scrolling the list, not the page", () => {
    const { scrollTo, scrollIntoView, restore } = stubScrollLayout();
    try {
      const { rerender } = render(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );
      scrollTo.mockClear();
      rerender(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );

      // The new line is revealed by scrolling the subtitle list itself; the
      // page-scrolling scrollIntoView is never used (it would yank the whole page).
      const calls = scrollTo.mock.calls.map(([opts]) => opts as ScrollToOptions);
      expect(scrollTo.mock.instances).toContain(subtitleList());
      expect(calls).toContainEqual({ top: 200, behavior: "smooth" });
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
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

  test("scrolls the selected statement into view within the list, not the page", () => {
    const { scrollTo, scrollIntoView, restore } = stubScrollLayout();
    try {
      renderWithPlayback(
        <LiveStatementList
          statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
          selectedStatementId="1"
        />,
      );

      expect(scrollTo.mock.instances).toContain(subtitleList());
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });
});
