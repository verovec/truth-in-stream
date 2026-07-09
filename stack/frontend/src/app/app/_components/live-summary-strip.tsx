"use client";

import { useLiveAnalysisSelector } from "@/components/live/live-analysis-provider";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveStatus } from "@/lib/live/session";
import type { LiveSummary } from "@/lib/live/summary";
import { useAppI18n, type AppDictionary } from "@/components/i18n/app-i18n";

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

// Each running count and the tone that ties claim verdicts to the same semantic
// verdict tokens the badges use. Labels come from the dictionary at render
// time; rendering from this config keeps the strip data-driven: a new stat is a
// new row here, not copied markup.
const STAT_TONES = {
  neutral: "text-ink dark:text-paper",
  positive: "text-verdict-credible",
  negative: "text-verdict-disputed",
  unclear: "text-verdict-flag dark:text-amber-300",
  unverifiable: "text-verdict-unverifiable",
  evidence: "text-bleu dark:text-sky-300",
  muted: "text-ink/50 dark:text-paper/50",
} as const;

const STATS: {
  key: keyof LiveSummary & keyof AppDictionary["summary"]["stats"];
  tone: keyof typeof STAT_TONES;
}[] = [
  { key: "checked", tone: "neutral" },
  { key: "corroborates", tone: "positive" },
  { key: "contradicts", tone: "negative" },
  { key: "unclear", tone: "unclear" },
  { key: "unverifiable", tone: "unverifiable" },
  { key: "evidence", tone: "evidence" },
  { key: "skipped", tone: "muted" },
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
  const { t } = useAppI18n();
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
      aria-label={t.summary.ariaLabel}
      className="flex w-full flex-wrap items-center gap-x-5 gap-y-2 rounded-2xl border border-black/10 bg-white px-4 py-3 dark:border-white/10 dark:bg-white/5"
    >
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {t.summary.heading}
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
              label={t.summary.stats[stat.key]}
              value={summary[stat.key]}
              tone={stat.tone}
            />
          ))}
          {summary.analysing > 0 && (
            <span className="text-xs text-ink/50 dark:text-paper/50">
              {formatTemplate(t.summary.inProgress, {
                count: summary.analysing,
              })}
            </span>
          )}
        </div>
      ) : (
        <p className="text-sm text-ink/50 dark:text-paper/50">
          {t.summary.idleHint}
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
      <span className="text-xs text-ink/50 dark:text-paper/50">{label}</span>
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
  const { t } = useAppI18n();
  if (!active) {
    return null;
  }
  if (status === "reconnecting") {
    return (
      <span className="inline-flex items-center rounded-full bg-verdict-flag/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300">
        {t.connection.reconnecting}
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className="inline-flex items-center rounded-full bg-rouge/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rouge dark:bg-rouge/15 dark:text-rose-300">
        {t.connection.interrupted}
      </span>
    );
  }
  if (status === "live") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full bg-rouge/10 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rouge dark:bg-rouge/15 dark:text-rouge-flag">
        <span className="size-1.5 animate-pulse rounded-full bg-rouge dark:bg-rouge-flag" />
        {t.connection.live}
      </span>
    );
  }
  return null;
}
