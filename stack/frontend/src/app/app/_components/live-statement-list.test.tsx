import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import { renderWithPlayback } from "@/test/playback";
import type { SkipReason } from "@/lib/fact-check/api";
import { fr } from "@/lib/i18n/dictionaries/fr";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveStatement } from "@/lib/live/statements";
import { stubScrollLayout } from "@/test/scroll-layout";
import { LiveStatementList } from "./live-statement-list";

// A provider-less render falls back to the French app dictionary, the product
// default; the subtitle strings below come straight from it.
const t = fr.app.subtitles;

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
  return screen.getByRole("list", { name: t.transcriptAria });
}

describe("LiveStatementList", () => {
  test("renders timestamps in tabular sans numerals, not a mono face", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "a checked statement")]}
        selectedStatementId={null}
      />,
    );
    // Timestamps read as clean UI numerals - tabular figures on the sans face -
    // rather than the terminal-like monospace they used to carry.
    const timestamp = subtitleList().querySelector("span.tabular-nums");
    expect(timestamp?.textContent).toMatch(/\d+:\d{2}/);
    expect(timestamp?.className).not.toContain("font-mono");
    expect(timestamp?.className).toContain("tabular-nums");
  });

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
                  verdict: "credible",
                },
              ]
            : []
        }
      />,
    );

    expect(screen.getByText(fr.app.claims.verdicts.credible)).toBeInTheDocument();
    expect(screen.getByText(fr.app.claims.sources.verified)).toBeInTheDocument();
    // The per-statement checking marker yields to the claim list.
    expect(screen.queryByText(t.checking)).not.toBeInTheDocument();
  });

  test("a legacy statement with no claims renders the generic marker as before", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
        claimsFor={() => []}
      />,
    );
    expect(screen.getByText(t.checking)).toBeInTheDocument();
  });

  test("shows an in-flight affordance for an analysing statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(/the earth is round/i)).toBeInTheDocument();
    expect(screen.getByText(t.checking)).toBeInTheDocument();
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

    expect(screen.getByText(`${t.speaker} A`)).toBeInTheDocument();
  });

  test("omits the speaker tag for an unattributed statement", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[analysing("0", 0, "the earth is round")]}
        selectedStatementId={null}
      />,
    );

    expect(
      screen.queryByText(new RegExp(t.speaker, "i")),
    ).not.toBeInTheDocument();
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

    expect(screen.getByText(t.checkFailed)).toBeInTheDocument();
  });

  test("notes a skipped statement's reason rather than a verdict", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "small talk", { skipReason: "not_a_claim" })]}
        selectedStatementId={null}
      />,
    );

    // The wire's not_a_claim reason renders through the dictionary's
    // notAClaim label inside the skipped template.
    expect(
      screen.getByText(
        formatTemplate(t.notChecked, { reason: t.skipReasons.notAClaim }),
      ),
    ).toBeInTheDocument();
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
      screen.getByText(
        formatTemplate(t.notChecked, { reason: t.skipReasons.unknown }),
      ),
    ).toBeInTheDocument();
  });

  test("notes when a checked statement found no confident match", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "obscure aside", { matches: [] })]}
        selectedStatementId={null}
      />,
    );

    expect(screen.getByText(t.noMatch)).toBeInTheDocument();
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
    expect(screen.getByText(t.corroborated)).toBeInTheDocument();
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

    expect(screen.getByText(`2 ${t.match.other}`)).toBeInTheDocument();
    expect(screen.getByText(`0.90 ${t.supporting}`)).toBeInTheDocument();
    expect(screen.getByText(`0.30 ${t.contradicting}`)).toBeInTheDocument();
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

    // French Intl.PluralRules reads 1 as singular; an exact match rejects the
    // plural form.
    expect(screen.getByText(`1 ${t.match.one}`)).toBeInTheDocument();
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

    expect(screen.queryByText(new RegExp(t.supporting, "i"))).toBeNull();
    expect(screen.queryByText(new RegExp(t.contradicting, "i"))).toBeNull();
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

    expect(screen.queryByText(t.corroborated)).toBeNull();
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

    expect(screen.getByText(t.contradictsEarlier)).toBeInTheDocument();
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

    expect(screen.queryByText(t.contradictsEarlier)).not.toBeInTheDocument();
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

  test("snaps the newest statement to the top while pinned, never scrolling the page", () => {
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

      // Pinned (resting at the top) by default: a new line snaps the subtitle list
      // back to its top instantly - no smooth animation that would read as a
      // down-then-back bounce - and never the page-scrolling scrollIntoView.
      const calls = scrollTo.mock.calls.map(([opts]) => opts as ScrollToOptions);
      expect(scrollTo.mock.instances).toContain(subtitleList());
      expect(calls).toContainEqual({ top: 0 });
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("freezes the scroll position once the operator scrolls away from the top", () => {
    const { scrollTo, restore } = stubScrollLayout();
    try {
      const { rerender } = render(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );
      // The operator scrolls down to read earlier lines, dropping the pin.
      fireEvent.scroll(subtitleList(), { target: { scrollTop: 500 } });
      scrollTo.mockClear();
      rerender(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );

      // A newly arrived statement must not move their view: no pin-to-top scroll.
      expect(scrollTo).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("re-pins and snaps to the top when the operator scrolls back up", () => {
    const { scrollTo, restore } = stubScrollLayout();
    try {
      const { rerender } = render(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );
      fireEvent.scroll(subtitleList(), { target: { scrollTop: 500 } });
      // Back to the top: the pin is restored.
      fireEvent.scroll(subtitleList(), { target: { scrollTop: 0 } });
      scrollTo.mockClear();
      rerender(
        <PlaybackProvider>
          <LiveStatementList
            statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
            selectedStatementId={null}
          />
        </PlaybackProvider>,
      );

      const calls = scrollTo.mock.calls.map(([opts]) => opts as ScrollToOptions);
      expect(calls).toContainEqual({ top: 0 });
    } finally {
      restore();
    }
  });

  test("marks the statement at the playback position active and selects, not seeks, on click", async () => {
    const onSelect = vi.fn();
    const { store } = renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
        selectedStatementId={null}
        onSelect={onSelect}
      />,
    );

    act(() => store.update({ currentTime: 11 }));

    const active = screen.getByText("second").closest("li");
    expect(active).toHaveAttribute("aria-current", "true");

    // Clicking a transcript line highlights it for inspection; it must never seek
    // the video, because a seek restarts the live session and wipes the running
    // speaker credibility and in-flight findings.
    const seek = vi.fn();
    store.registerSeekHandler(seek);
    await userEvent.click(screen.getByText("first"));
    expect(onSelect).toHaveBeenCalledWith("0");
    expect(seek).not.toHaveBeenCalled();
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
