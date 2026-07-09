"use client";

import { useCallback, useMemo, useState } from "react";
import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import {
  type SeparatorProps,
  useVerticalSplit,
} from "@/hooks/use-vertical-split";
import type { LiveClaim } from "@/lib/live/claims";
import { deriveFactChecks } from "@/lib/live/fact-checks";
import type { LiveStatus } from "@/lib/live/session";
import type { LiveStatement } from "@/lib/live/statements";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { LiveFactCheckList } from "./live-fact-check-list";
import { LiveStatementList } from "./live-statement-list";
import { PlaybackClock } from "./playback-clock";

// EMPTY is a stable empty-statements reference so the selector returns the same
// value every render while no video is being analysed, keeping
// useSyncExternalStore from looping.
const EMPTY: LiveStatement[] = [];

// NO_CLAIMS is a stable empty result for claimsFor while no session is active,
// so the subtitle list reads the same identity every render rather than a fresh
// closure that would re-render the memoized list each tick.
const EMPTY_CLAIMS: LiveClaim[] = [];
function NO_CLAIMS(): LiveClaim[] {
  return EMPTY_CLAIMS;
}

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
  // claimsFor keeps a stable identity across interim/statement-only updates (it
  // changes only when the claims store changes), so the memoized statement list
  // re-renders only when a claim actually progresses.
  const claimsFor = useLiveAnalysisSelector(
    (snapshot) => snapshot?.claimsFor ?? NO_CLAIMS,
  );
  // tick increments on every selection so re-selecting the same fact-check entry
  // still scrolls its origin subtitle back into view.
  const [selection, setSelection] = useState<{
    id: string;
    tick: number;
  } | null>(null);
  // Selecting a statement - from either the fact-check list or a transcript line -
  // lifts its id here so the line and its fact-check entry highlight together and
  // the subtitle scrolls into view. Stable identity (the updater is functional, no
  // external deps) so the memoized lists do not re-render on every interim word.
  const selectStatement = useCallback(
    (statementId: string) =>
      setSelection((prev) => ({ id: statementId, tick: (prev?.tick ?? 0) + 1 })),
    [],
  );
  // Derived from the same statements and claims that drive the subtitles, so the
  // two can never disagree. On the verify path the verdicts live in the claims
  // store (statements stay "analysing"), so the list must read claimsFor to fill
  // at all. Memoized (the React compiler is not enabled) on both inputs - and
  // claimsFor keeps a stable identity except when a claim progresses - so the
  // interim caption updating on every spoken word does not re-derive or re-render
  // the memoized fact-check list.
  const entries = useMemo(
    () => deriveFactChecks(statements, claimsFor),
    [statements, claimsFor],
  );

  const { t } = useAppI18n();

  // The operator can drag (or arrow-key) the divider to trade height between the
  // transcript and the fact-check list, so a long transcript and a long verdict
  // list can each be given room without the panel growing.
  const { containerRef, topGrow, bottomGrow, separatorProps } =
    useVerticalSplit(t.panel.separator);

  return (
    <aside
      aria-labelledby="live-analysis-heading"
      // Sticky under the sticky app header so the panel stays on screen as the
      // page scrolls. Its outer size is derived only from the viewport and the
      // header-height token (--app-header-h), never from the summary or speaker
      // strips above it, so incoming statements, verdicts and speakers can grow
      // those strips without ever resizing this panel - content scrolls inside
      // instead. Pinned just below the header (top clears the header height plus a
      // small gap); the matching height keeps the bottom within the viewport once
      // pinned. Works because the grid is items-start, so the taller left column
      // gives this column travel to stick.
      className="sticky top-[calc(var(--app-header-h)+0.5rem)] flex h-[calc(100svh-var(--app-header-h)-1rem)] flex-col gap-3 rounded-2xl border border-black/10 bg-white p-4 dark:border-white/10 dark:bg-white/5"
    >
      <header className="flex items-baseline justify-between gap-2">
        <div className="flex items-center gap-2">
          <h2
            id="live-analysis-heading"
            className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60"
          >
            {t.panel.heading}
          </h2>
          <LiveStatusPill status={status} />
        </div>
        <PlaybackClock />
      </header>
      <ConnectionNotice status={status} />

      {/* The two regions split the remaining height by the draggable divider's
          ratio. flexGrow is an inline style, not a class, because it is a dynamic
          numeric weight Tailwind cannot express; everything else stays in
          classes. */}
      <div ref={containerRef} className="flex min-h-0 flex-1 flex-col">
        <section
          aria-label={t.panel.subtitlesAria}
          style={{ flexGrow: topGrow, flexBasis: 0 }}
          className="flex min-h-0 flex-col gap-2 overflow-hidden"
        >
          <RegionHeading>{t.panel.subtitles}</RegionHeading>
          {/* The interim caption is the live utterance, newer than any committed
              statement, so it sits at the top above the newest-first list. */}
          <LiveCaption text={caption} />
          {statements.length > 0 && (
            <LiveStatementList
              statements={statements}
              selectedStatementId={selection?.id ?? null}
              selectionTick={selection?.tick ?? 0}
              claimsFor={claimsFor}
              onSelect={selectStatement}
            />
          )}
          {statements.length === 0 && !caption && <EmptyHint status={status} />}
        </section>

        <PanelSeparator {...separatorProps} />

        {/* The fact-checks region sits in a faintly-tinted, rounded tray bled to
            the panel edges so it reads as a zone distinct from the free-flowing
            transcript above, rather than the two blurring into one column. The
            negative margins cancel the panel's padding so the tint reaches the
            border; the padding is added back so the content keeps its inset. */}
        <section
          aria-label={t.panel.factChecks}
          style={{ flexGrow: bottomGrow, flexBasis: 0 }}
          className="-mx-4 -mb-4 flex min-h-0 flex-col gap-2 overflow-hidden rounded-b-2xl bg-ink/[0.03] px-4 pb-4 pt-3 dark:bg-night/40"
        >
          <RegionHeading>{t.panel.factChecks}</RegionHeading>
          <LiveFactCheckList
            entries={entries}
            selectedStatementId={selection?.id ?? null}
            onSelect={selectStatement}
          />
        </section>
      </div>
    </aside>
  );
}

// PanelSeparator is the draggable divider between the transcript and the
// fact-check list. It is the only resize affordance: a grip-marked line with a
// row-resize cursor that the hook makes operable by pointer and keyboard.
function PanelSeparator(props: SeparatorProps) {
  return (
    <div
      {...props}
      className="group relative flex h-3 shrink-0 cursor-row-resize touch-none items-center justify-center rounded focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60"
    >
      <span className="h-px w-full bg-black/10 transition-colors group-hover:bg-bleu-flag dark:bg-white/10 dark:group-hover:bg-sky-400/60" />
      <span className="absolute h-1 w-12 rounded-full bg-black/15 transition-colors group-hover:bg-bleu-flag group-focus-visible:bg-bleu-flag dark:bg-white/15 dark:group-hover:bg-sky-400/60" />
    </div>
  );
}

// RegionHeading labels each of the two stacked scroll regions. shrink-0 keeps
// the label from being squeezed when its region is dragged to the minimum.
function RegionHeading({ children }: { children: string }) {
  return (
    <h3 className="shrink-0 text-[11px] font-semibold uppercase tracking-wider text-ink/50 dark:text-paper/50">
      {children}
    </h3>
  );
}

// LiveCaption shows the current utterance as it is spoken, before it commits to
// a statement, so the transcript is visible word by word. It sits at the top of
// the subtitle region, above the newest committed statement, and renders nothing
// between utterances.
function LiveCaption({ text }: { text: string }) {
  if (!text) {
    return null;
  }
  return (
    <p
      aria-live="polite"
      className="flex items-start gap-2 border-b border-dashed border-black/10 pb-2 text-sm italic leading-5 text-ink/50 dark:border-white/10 dark:text-paper/50"
    >
      <span
        aria-hidden="true"
        className="mt-1.5 size-1.5 shrink-0 animate-pulse rounded-full bg-rouge dark:bg-rouge-flag"
      />
      {/* Clamped to two lines so a long interim utterance can never grow the
          subtitle region and push the committed transcript; the full text lands
          in the transcript the moment the utterance commits. */}
      <span className="line-clamp-2">{text}</span>
    </p>
  );
}

// LiveStatusPill is the small live/reconnecting indicator next to the heading.
function LiveStatusPill({ status }: { status: LiveStatus }) {
  const { t } = useAppI18n();
  if (status === "live") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-rouge/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rouge dark:bg-rouge/15 dark:text-rouge-flag">
        <span className="size-1.5 animate-pulse rounded-full bg-rouge dark:bg-rouge-flag" />
        {t.connection.live}
      </span>
    );
  }
  if (status === "reconnecting") {
    return (
      <span className="inline-flex items-center rounded-full bg-verdict-flag/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300">
        {t.connection.reconnecting}
      </span>
    );
  }
  return null;
}

// ConnectionNotice surfaces interruptions without blocking playback or hiding
// the verdicts already on screen.
function ConnectionNotice({ status }: { status: LiveStatus }) {
  const { t } = useAppI18n();
  if (status === "error") {
    return (
      <p
        role="alert"
        className="rounded-lg border border-rouge/25 bg-rouge/5 px-3 py-2 text-sm text-rouge dark:border-rouge/40 dark:bg-rouge/15 dark:text-rose-300"
      >
        {t.panel.interrupted}
      </p>
    );
  }
  if (status === "reconnecting") {
    return (
      <p className="text-xs text-verdict-flag dark:text-amber-300">
        {t.panel.reconnecting}
      </p>
    );
  }
  return null;
}

// EmptyHint explains, per status, why no subtitles are shown yet.
function EmptyHint({ status }: { status: LiveStatus }) {
  const { t } = useAppI18n();
  const message =
    status === "connecting"
      ? t.panel.hints.connecting
      : status === "live"
        ? t.panel.hints.listening
        : status === "ended"
          ? t.panel.hints.ended
          : t.panel.hints.idle;
  return <p className="text-sm text-ink/60 dark:text-paper/60">{message}</p>;
}
