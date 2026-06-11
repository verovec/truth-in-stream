"use client";

import { useCallback, useMemo, useState } from "react";
import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import { deriveFactChecks } from "@/lib/live/fact-checks";
import type { LiveStatus } from "@/lib/live/session";
import type { LiveStatement } from "@/lib/live/statements";
import { LiveFactCheckList } from "./live-fact-check-list";
import { LiveStatementList } from "./live-statement-list";
import { PlaybackClock } from "./playback-clock";

// EMPTY is a stable empty-statements reference so the selector returns the same
// value every render while no video is being analysed, keeping
// useSyncExternalStore from looping.
const EMPTY: LiveStatement[] = [];

// LiveFactCheckPanel feeds the live analysis stream for the selected video into
// two stacked, independently-scrolling regions inside one fixed-height
// container: subtitles on top, a decoupled fact-check list below. The panel
// height is a fraction of the viewport (not content- or player-driven), so a
// long transcript never pushes the fact-checks off screen and vice versa.
// Selecting a fact-check entry lifts its statement id here so the subtitle
// region can highlight and scroll the origin into view. It reads the shared
// live snapshot, so it and the top-of-page summary strip track one session over
// one WebSocket.
export function LiveFactCheckPanel() {
  const statements = useLiveAnalysisSelector(
    (snapshot) => snapshot?.statements ?? EMPTY,
  );
  const caption = useLiveAnalysisSelector(
    (snapshot) => snapshot?.caption ?? "",
  );
  const status = useLiveAnalysisSelector(
    (snapshot) => snapshot?.status ?? "idle",
  );
  // tick increments on every selection so re-selecting the same fact-check entry
  // still scrolls its origin subtitle back into view.
  const [selection, setSelection] = useState<{
    id: string;
    tick: number;
  } | null>(null);
  // Stable identity (the updater is functional, no external deps) so the
  // memoized fact-check list does not re-render on every interim caption word.
  const selectFactCheck = useCallback(
    (statementId: string) =>
      setSelection((prev) => ({ id: statementId, tick: (prev?.tick ?? 0) + 1 })),
    [],
  );
  // Derived from the same statements that drive the subtitles, so the two can
  // never disagree. Memoized (the React compiler is not enabled) so the interim
  // caption updating on every spoken word does not re-derive or re-render the
  // memoized fact-check list.
  const entries = useMemo(() => deriveFactChecks(statements), [statements]);

  return (
    <aside
      aria-labelledby="live-analysis-heading"
      className="flex h-[78svh] flex-col gap-3 rounded-xl border border-zinc-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <header className="flex items-baseline justify-between gap-2">
        <div className="flex items-center gap-2">
          <h2
            id="live-analysis-heading"
            className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100"
          >
            Live analysis
          </h2>
          <LiveStatusPill status={status} />
        </div>
        <PlaybackClock />
      </header>
      <ConnectionNotice status={status} />

      <section
        aria-label="Live subtitles"
        className="flex min-h-0 flex-1 flex-col gap-2"
      >
        <RegionHeading>Subtitles</RegionHeading>
        {statements.length > 0 && (
          <LiveStatementList
            statements={statements}
            selectedStatementId={selection?.id ?? null}
            selectionTick={selection?.tick ?? 0}
          />
        )}
        {statements.length === 0 && !caption && <EmptyHint status={status} />}
        <LiveCaption text={caption} />
      </section>

      <section
        aria-label="Fact checks"
        className="flex min-h-0 flex-1 flex-col gap-2 border-t border-zinc-200 pt-3 dark:border-zinc-800"
      >
        <RegionHeading>Fact checks</RegionHeading>
        <LiveFactCheckList
          entries={entries}
          selectedStatementId={selection?.id ?? null}
          onSelect={selectFactCheck}
        />
      </section>
    </aside>
  );
}

// RegionHeading labels each of the two stacked scroll regions.
function RegionHeading({ children }: { children: string }) {
  return (
    <h3 className="text-[11px] font-semibold uppercase tracking-wide text-zinc-500 dark:text-zinc-400">
      {children}
    </h3>
  );
}

// LiveCaption shows the current utterance as it is spoken, before it commits to
// a statement, so the transcript is visible word by word. It is part of the
// subtitle region and renders nothing between utterances.
function LiveCaption({ text }: { text: string }) {
  if (!text) {
    return null;
  }
  return (
    <p
      aria-live="polite"
      className="mt-auto flex items-start gap-2 border-t border-dashed border-zinc-200 pt-2 text-sm italic leading-5 text-zinc-500 dark:border-zinc-800 dark:text-zinc-400"
    >
      <span
        aria-hidden="true"
        className="mt-1.5 size-1.5 shrink-0 animate-pulse rounded-full bg-rose-500"
      />
      {text}
    </p>
  );
}

// LiveStatusPill is the small live/reconnecting indicator next to the heading.
function LiveStatusPill({ status }: { status: LiveStatus }) {
  if (status === "live") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rose-700 dark:bg-rose-500/15 dark:text-rose-300">
        <span className="size-1.5 animate-pulse rounded-full bg-rose-500" />
        Live
      </span>
    );
  }
  if (status === "reconnecting") {
    return (
      <span className="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:bg-amber-500/15 dark:text-amber-300">
        Reconnecting
      </span>
    );
  }
  return null;
}

// ConnectionNotice surfaces interruptions without blocking playback or hiding
// the verdicts already on screen.
function ConnectionNotice({ status }: { status: LiveStatus }) {
  if (status === "error") {
    return (
      <p
        role="alert"
        className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-800 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
      >
        Live analysis was interrupted. Playback continues; press play again to
        retry.
      </p>
    );
  }
  if (status === "reconnecting") {
    return (
      <p className="text-xs text-amber-700 dark:text-amber-400">
        Connection lost. Reconnecting…
      </p>
    );
  }
  return null;
}

// EmptyHint explains, per status, why no subtitles are shown yet.
function EmptyHint({ status }: { status: LiveStatus }) {
  const message =
    status === "connecting"
      ? "Connecting to live analysis…"
      : status === "live"
        ? "Listening for spoken claims…"
        : status === "ended"
          ? "The stream ended with no checked statements."
          : "Fact checks stream here while the video plays.";
  return <p className="text-sm text-zinc-600 dark:text-zinc-400">{message}</p>;
}
