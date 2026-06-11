"use client";

import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import type { LiveStatus } from "@/lib/live/session";
import type { LiveSummary } from "@/lib/live/summary";

// LiveSummaryStrip is the full-width running-findings bar at the very top of the
// analyser. It reads the shared live snapshot through a selector, so it tracks
// the fact-check list without a second WebSocket and re-renders only when the
// summary or connection status changes, not on every interim caption word.
export function LiveSummaryStrip() {
  const summary = useLiveAnalysisSelector((snapshot) => snapshot?.summary ?? null);
  const status = useLiveAnalysisSelector(
    (snapshot) => snapshot?.status ?? "idle",
  );
  return <SummaryStripView summary={summary} status={status} />;
}

// Each running count, its label, and the tone that ties claim verdicts to the
// same colours the verdict badges use. Rendering from this config keeps the
// strip data-driven: a new stat is a new row here, not copied markup.
const STAT_TONES = {
  neutral: "text-zinc-900 dark:text-zinc-100",
  positive: "text-emerald-700 dark:text-emerald-300",
  negative: "text-rose-700 dark:text-rose-300",
  unclear: "text-amber-700 dark:text-amber-300",
  evidence: "text-sky-700 dark:text-sky-300",
  muted: "text-zinc-500 dark:text-zinc-400",
} as const;

const STATS: {
  key: keyof LiveSummary;
  label: string;
  tone: keyof typeof STAT_TONES;
}[] = [
  { key: "checked", label: "Checked", tone: "neutral" },
  { key: "corroborates", label: "Corroborated", tone: "positive" },
  { key: "contradicts", label: "Contradicted", tone: "negative" },
  { key: "unclear", label: "Unclear", tone: "unclear" },
  { key: "evidence", label: "Evidence", tone: "evidence" },
  { key: "skipped", label: "Not checked", tone: "muted" },
];

// SummaryStripView is the presentational strip. A null summary is the idle state
// (no video being analysed); otherwise it shows the running counts and, when the
// connection drops, a reconnecting indicator without losing the counts.
export function SummaryStripView({
  summary,
  status,
}: {
  summary: LiveSummary | null;
  status: LiveStatus;
}) {
  // Quiet only when there is no active session, or a selected video that has not
  // started playing and produced nothing yet. Once the session leaves idle -
  // playing, paused after playing, reconnecting, ended, or interrupted - show
  // the running tally and let the indicator reflect any connection trouble, so
  // the strip never reads "all clear" while the panel shows an error.
  const idle =
    summary === null ||
    (status === "idle" &&
      summary.checked + summary.skipped + summary.analysing === 0);

  return (
    <section
      aria-label="Live findings summary"
      className="flex w-full flex-wrap items-center gap-x-5 gap-y-2 rounded-xl border border-zinc-200 bg-white px-4 py-3 dark:border-zinc-800 dark:bg-zinc-950"
    >
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-zinc-900 dark:text-zinc-100">
          Live findings
        </h2>
        <ConnectionIndicator status={status} active={!idle} />
      </div>
      {summary !== null && !idle ? (
        <div
          aria-live="polite"
          className="flex flex-1 flex-wrap items-center gap-x-5 gap-y-1"
        >
          {STATS.map((stat) => (
            <Stat
              key={stat.key}
              label={stat.label}
              value={summary[stat.key]}
              tone={stat.tone}
            />
          ))}
          {summary.analysing > 0 && (
            <span className="text-xs text-zinc-500 dark:text-zinc-400">
              {summary.analysing} in progress
            </span>
          )}
        </div>
      ) : (
        <p className="text-sm text-zinc-500 dark:text-zinc-400">
          Findings appear here as the video is analysed.
        </p>
      )}
    </section>
  );
}

// Stat is one labelled count. The aria-label pairs the number with its meaning
// so the polite live region announces "Corroborated: 3" rather than a bare "3".
function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: keyof typeof STAT_TONES;
}) {
  return (
    <div
      aria-label={`${label}: ${value}`}
      className="flex items-baseline gap-1.5"
    >
      <span className={`text-base font-semibold tabular-nums ${STAT_TONES[tone]}`}>
        {value}
      </span>
      <span className="text-xs text-zinc-500 dark:text-zinc-400">{label}</span>
    </div>
  );
}

// ConnectionIndicator mirrors the panel's live/reconnecting cues so the strip
// reflects the session state. It is quiet when there is no active session.
function ConnectionIndicator({
  status,
  active,
}: {
  status: LiveStatus;
  active: boolean;
}) {
  if (!active) {
    return null;
  }
  if (status === "reconnecting") {
    return (
      <span className="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700 dark:bg-amber-500/15 dark:text-amber-300">
        Reconnecting
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className="inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rose-700 dark:bg-rose-500/15 dark:text-rose-300">
        Interrupted
      </span>
    );
  }
  if (status === "live") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rose-700 dark:bg-rose-500/15 dark:text-rose-300">
        <span className="size-1.5 animate-pulse rounded-full bg-rose-500" />
        Live
      </span>
    );
  }
  return null;
}
