import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";
import { PlaybackProvider } from "@/components/playback/playback-provider";
import {
  TranscriptDisplayProvider,
  useTranscriptDisplay,
} from "@/components/live/transcript-display";
import { renderWithPlayback } from "@/test/playback";
import { fr } from "@/lib/i18n/dictionaries/fr";
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

// ToggleProbe exposes the display context's toggle as a plain button so a test
// can flip the unverified-highlights preference the way the strip does.
function ToggleProbe() {
  const { toggleUnverified } = useTranscriptDisplay();
  return (
    <button type="button" onClick={toggleUnverified}>
      probe-toggle
    </button>
  );
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

  test("marks the exact words a claim was checked against, tinted by its verdict", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "Le chomage a baisse fortement.")]}
        selectedStatementId={null}
        highlightsFor={(segmentId) =>
          segmentId === "0"
            ? [
                {
                  unitId: "0",
                  claimId: "0-0",
                  start: 3,
                  end: 19,
                  status: "verified",
                  verdict: "disputed",
                },
              ]
            : []
        }
      />,
    );
    const mark = subtitleList().querySelector("mark");
    expect(mark?.textContent).toBe("chomage a baisse");
    expect(mark?.getAttribute("data-claim-id")).toBe("0-0");
    expect(mark?.className).toContain("bg-verdict-disputed");
    // The sentence's full text is intact around the mark.
    expect(subtitleList().textContent).toContain(
      "Le chomage a baisse fortement.",
    );
  });

  test("a corroborated claim marks green", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "Le chomage a baisse fortement.")]}
        selectedStatementId={null}
        highlightsFor={() => [
          {
            unitId: "0",
            claimId: "0-0",
            start: 3,
            end: 19,
            status: "verified",
            verdict: "credible",
          },
        ]}
      />,
    );
    const mark = subtitleList().querySelector("mark");
    expect(mark?.textContent).toBe("chomage a baisse");
    expect(mark?.className).toContain("bg-verdict-credible");
  });

  test("a verified-unverifiable highlight renders plain by default", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "Le chomage a baisse fortement.")]}
        selectedStatementId={null}
        highlightsFor={() => [
          {
            unitId: "0",
            claimId: "0-0",
            start: 3,
            end: 19,
            status: "verified",
            verdict: "unverifiable",
          },
        ]}
      />,
    );
    // By default only corroborated and contradicted claims mark the text; an
    // unverifiable verdict stays plain until the viewer opts in.
    expect(subtitleList().querySelector("mark")).toBeNull();
    expect(subtitleList().textContent).toContain(
      "Le chomage a baisse fortement.",
    );
  });

  test("the unverified toggle reveals unverifiable marks, muted", async () => {
    render(
      <TranscriptDisplayProvider>
        <PlaybackProvider>
          <ToggleProbe />
          <LiveStatementList
            statements={[checked("0", 0, "Le chomage a baisse fortement.")]}
            selectedStatementId={null}
            highlightsFor={() => [
              {
                unitId: "0",
                claimId: "0-0",
                start: 3,
                end: 19,
                status: "verified",
                verdict: "unverifiable",
              },
            ]}
          />
        </PlaybackProvider>
      </TranscriptDisplayProvider>,
    );
    expect(subtitleList().querySelector("mark")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "probe-toggle" }));
    const mark = subtitleList().querySelector("mark");
    expect(mark?.textContent).toBe("chomage a baisse");
    expect(mark?.className).toContain("bg-verdict-unverifiable");

    // Toggling back off hides the mark again; the text never disappears.
    await userEvent.click(screen.getByRole("button", { name: "probe-toggle" }));
    expect(subtitleList().querySelector("mark")).toBeNull();
    expect(subtitleList().textContent).toContain(
      "Le chomage a baisse fortement.",
    );
  });

  test("pending, checking, and shed claims render plain text, no wash", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "Deux affirmations distinctes ici.")]}
        selectedStatementId={null}
        highlightsFor={() => [
          {
            unitId: "0",
            claimId: "0-0",
            start: 0,
            end: 4,
            status: "pending",
          },
          {
            unitId: "0",
            claimId: "0-1",
            start: 5,
            end: 17,
            status: "checking",
          },
          {
            unitId: "0",
            claimId: "0-2",
            start: 18,
            end: 27,
            status: "unchecked",
          },
        ]}
      />,
    );
    // An unresolved claim asserts nothing yet, so nothing is marked and the
    // whole sentence stays readable.
    expect(subtitleList().querySelectorAll("mark")).toHaveLength(0);
    expect(subtitleList().textContent).toContain(
      "Deux affirmations distinctes ici.",
    );
  });

  test("a merged unit flows sentence by sentence with per-member highlights", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          {
            ...analysing("0", 0, "Le budget monte. Il monte de dix pour cent."),
            speaker: "A",
            end: 6,
            parts: [
              { id: "0", text: "Le budget monte.", start: 0, end: 3 },
              {
                id: "1",
                text: "Il monte de dix pour cent.",
                start: 3,
                end: 6,
              },
            ],
          },
        ]}
        selectedStatementId={null}
        highlightsFor={(segmentId) =>
          segmentId === "1"
            ? [
                {
                  unitId: "0",
                  claimId: "0-0",
                  start: 12,
                  end: 25,
                  status: "verified",
                  verdict: "credible",
                },
              ]
            : []
        }
      />,
    );
    // One speaker turn, with each member sentence its own inline click target -
    // no per-unit block.
    const items = subtitleList().querySelectorAll("li");
    expect(items).toHaveLength(1);
    const sentences = items[0].querySelectorAll("p button");
    expect(sentences).toHaveLength(2);
    expect(sentences[0].textContent).toBe("Le budget monte.");
    expect(sentences[1].textContent).toBe("Il monte de dix pour cent.");
    // The second member's span still anchors by its own segment offsets.
    const mark = items[0].querySelector("mark");
    expect(mark?.textContent).toBe("dix pour cent");
    expect(mark?.className).toContain("bg-verdict-credible");
  });

  test("consecutive statements by one speaker flow as one labelled turn", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          { ...checked("0", 0, "Première phrase."), speaker: "A" },
          { ...checked("1", 3, "Deuxième phrase."), speaker: "A" },
          { ...checked("2", 6, "Autre voix."), speaker: "B" },
        ]}
        selectedStatementId={null}
      />,
    );
    const turns = screen.getAllByRole("listitem");
    expect(turns).toHaveLength(2);
    expect(turns[0].textContent).toContain(`${t.speaker} A`);
    expect(turns[0].textContent).toContain("Première phrase.");
    expect(turns[0].textContent).toContain("Deuxième phrase.");
    expect(turns[1].textContent).toContain(`${t.speaker} B`);
    expect(turns[1].textContent).toContain("Autre voix.");
    // One speaker label per turn, not one per sentence.
    expect(screen.getAllByText(`${t.speaker} A`)).toHaveLength(1);
  });

  test("unattributed statements flow as one unlabelled paragraph", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          checked("0", 0, "the earth is round"),
          checked("1", 3, "the sky is blue"),
        ]}
        selectedStatementId={null}
      />,
    );
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(
      screen.queryByText(new RegExp(t.speaker, "i")),
    ).not.toBeInTheDocument();
  });

  test("shows no per-statement status markers; verdict detail lives in the fact-check region", () => {
    renderWithPlayback(
      <LiveStatementList
        statements={[
          analysing("0", 0, "the earth is round"),
          checked("1", 3, "garbled", { error: "analysis failed" }),
          checked("2", 6, "small talk", { skipReason: "not_a_claim" }),
          checked("3", 9, "the moon is rock", {
            matches: [
              {
                kind: "claim",
                claim: "Earth is an oblate spheroid",
                verdict: "corroborates",
                sources: [{ title: "NASA", url: "https://nasa.gov" }],
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
    // Every sentence is displayed, always - analysing, errored, skipped, and
    // scored alike - as plain flowing text without status paragraphs.
    for (const text of [
      "the earth is round",
      "garbled",
      "small talk",
      "the moon is rock",
    ]) {
      expect(subtitleList().textContent).toContain(text);
    }
    expect(screen.queryByText(/82%/)).not.toBeInTheDocument();
    expect(screen.queryByText(/oblate spheroid/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "NASA" })).not.toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
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

  test("the inconsistency link scrolls the earlier sentence into view within the list, not the page", async () => {
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

  test("reads chronologically: oldest text first, newest last", () => {
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

    const text = subtitleList().textContent ?? "";
    const oldest = text.indexOf("the oldest remark");
    const middle = text.indexOf("a middle remark");
    const newest = text.indexOf("the newest remark");
    expect(oldest).toBeGreaterThanOrEqual(0);
    expect(oldest).toBeLessThan(middle);
    expect(middle).toBeLessThan(newest);
  });

  test("snaps the newest text to the bottom while pinned, never scrolling the page", () => {
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

      // Pinned (resting at the bottom) by default: new text snaps the subtitle
      // list back to its bottom instantly - no smooth animation - and never the
      // page-scrolling scrollIntoView.
      const calls = scrollTo.mock.calls.map(([opts]) => opts as ScrollToOptions);
      expect(scrollTo.mock.instances).toContain(subtitleList());
      expect(calls).toContainEqual({ top: 1000 });
      expect(scrollIntoView).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("freezes the scroll position once the operator scrolls away from the bottom", () => {
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
      // The operator scrolls up to read earlier lines, dropping the pin (the
      // stubbed list rests at the bottom at scrollTop 900).
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

      // Newly arrived text must not move their view: no pin-to-bottom scroll.
      expect(scrollTo).not.toHaveBeenCalled();
    } finally {
      restore();
    }
  });

  test("re-pins and snaps to the bottom when the operator scrolls back down", () => {
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
      // Back to the bottom: the pin is restored.
      fireEvent.scroll(subtitleList(), { target: { scrollTop: 900 } });
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
      expect(calls).toContainEqual({ top: 1000 });
    } finally {
      restore();
    }
  });

  test("marks the sentence at the playback position active and selects, not seeks, on click", async () => {
    const onSelect = vi.fn();
    const { store } = renderWithPlayback(
      <LiveStatementList
        statements={[checked("0", 0, "first"), checked("1", 10, "second")]}
        selectedStatementId={null}
        onSelect={onSelect}
      />,
    );

    act(() => store.update({ currentTime: 11 }));

    const active = screen.getByText("second").closest("button");
    expect(active).toHaveAttribute("aria-current", "true");
    expect(screen.getByText("first").closest("button")).not.toHaveAttribute(
      "aria-current",
    );

    // Clicking a sentence highlights it for inspection; it must never seek the
    // video, because a seek restarts the live session and wipes the running
    // speaker credibility and in-flight findings.
    const seek = vi.fn();
    store.registerSeekHandler(seek);
    await userEvent.click(screen.getByText("first"));
    expect(onSelect).toHaveBeenCalledWith("0");
    expect(seek).not.toHaveBeenCalled();
  });

  test("tracks the playback position at sentence granularity inside a merged unit", () => {
    const { store } = renderWithPlayback(
      <LiveStatementList
        statements={[
          {
            ...analysing("0", 0, "Le budget monte. Il monte de dix pour cent."),
            end: 6,
            parts: [
              { id: "0", text: "Le budget monte.", start: 0, end: 3 },
              {
                id: "1",
                text: "Il monte de dix pour cent.",
                start: 3,
                end: 6,
              },
            ],
          },
        ]}
        selectedStatementId={null}
      />,
    );

    act(() => store.update({ currentTime: 4 }));

    // Only the member sentence containing the position is active, not the whole
    // unit - the transcript never regresses to block-level tracking.
    expect(
      screen.getByText("Il monte de dix pour cent.").closest("button"),
    ).toHaveAttribute("aria-current", "true");
    expect(
      screen.getByText("Le budget monte.").closest("button"),
    ).not.toHaveAttribute("aria-current");
  });

  test("a click on any member sentence of a merged unit selects the unit's statement", async () => {
    const onSelect = vi.fn();
    renderWithPlayback(
      <LiveStatementList
        statements={[
          {
            ...analysing("0", 0, "Le budget monte. Il monte de dix pour cent."),
            end: 6,
            parts: [
              { id: "0", text: "Le budget monte.", start: 0, end: 3 },
              {
                id: "1",
                text: "Il monte de dix pour cent.",
                start: 3,
                end: 6,
              },
            ],
          },
        ]}
        selectedStatementId={null}
        onSelect={onSelect}
      />,
    );

    await userEvent.click(screen.getByText("Il monte de dix pour cent."));
    // The non-anchor member still selects the unit's id - the id its fact-check
    // entry is keyed on.
    expect(onSelect).toHaveBeenCalledWith("0");
  });

  test("scrolls the selected statement's sentence into view within the list, not the page", () => {
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
