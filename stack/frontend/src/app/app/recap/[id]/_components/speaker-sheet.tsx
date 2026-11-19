"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import { formatTime } from "@/lib/playback/format-time";
import {
  formatDuration,
  QUOTE_LIMIT,
  type RecapSpeaker,
  type RecapStatement,
  truncateQuote,
} from "@/lib/video/recap";
import { DistributionBar } from "./distribution-bar";
import { speakerDisplayName } from "@/lib/video/speaker-display-name";

// SpeakerSheet is one principal's page of the report: the same figures as the
// recap row, the distribution, then the disputed and the credible statements
// with their justification and sources. Unverifiable and not-verified
// statements are counted on the sheet, never silently dropped.
export function SpeakerSheet({ speaker }: { speaker: RecapSpeaker }) {
  const { t } = useAppI18n();
  const copy = t.recap.sheet;
  const columns = t.recap.table;
  const name = speakerDisplayName(speaker, t);
  const headingId = `recap-sheet-${speaker.label}`;
  const figures = [
    { label: columns.speakingTime, value: formatDuration(speaker.speakingTime) },
    { label: columns.share, value: `${Math.round(speaker.share * 100)} %` },
    { label: columns.turns, value: speaker.turns },
    { label: columns.detected, value: speaker.counts.detected },
    { label: columns.credible, value: speaker.counts.credible },
    { label: columns.disputed, value: speaker.counts.disputed },
    { label: columns.unverifiable, value: speaker.counts.unverifiable },
    { label: columns.notVerified, value: speaker.counts.notVerified },
  ];
  return (
    <section
      aria-labelledby={headingId}
      className="flex flex-col gap-4 rounded-2xl border border-black/10 bg-white p-5 sm:p-8 dark:border-white/10 dark:bg-white/5 print:break-before-page"
    >
      <div className="flex flex-col gap-0.5">
        <h3
          id={headingId}
          aria-label={formatTemplate(copy.aria, { name })}
          className="font-display text-2xl font-semibold tracking-tight"
        >
          {name}
        </h3>
        {speaker.name !== "" ? (
          <p className="text-xs text-ink/60 dark:text-paper/60">
            {speaker.nameConfirmed ? copy.confirmedName : copy.automaticName}
          </p>
        ) : null}
      </div>
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-4">
        {figures.map((figure) => (
          <div key={figure.label} className="flex flex-col-reverse">
            <dt className="text-xs text-ink/60 dark:text-paper/60">
              {figure.label}
            </dt>
            <dd className="text-lg font-semibold tabular-nums">{figure.value}</dd>
          </div>
        ))}
      </dl>
      <DistributionBar counts={speaker.counts} />
      <StatementList
        heading={copy.disputedHeading}
        empty={copy.noDisputed}
        statements={speaker.disputed}
      />
      <StatementList
        heading={copy.credibleHeading}
        empty={copy.noCredible}
        statements={speaker.credible}
      />
      {speaker.counts.unverifiable + speaker.counts.notVerified > 0 ? (
        <p className="text-xs text-ink/60 dark:text-paper/60">
          {formatTemplate(copy.uncounted, {
            unverifiable: speaker.counts.unverifiable,
            notVerified: speaker.counts.notVerified,
          })}
        </p>
      ) : null}
    </section>
  );
}

function StatementList({
  heading,
  empty,
  statements,
}: {
  heading: string;
  empty: string;
  statements: RecapStatement[];
}) {
  return (
    <div className="flex flex-col gap-2">
      <h4 className="text-sm font-semibold uppercase tracking-wide text-ink/70 dark:text-paper/70">
        {heading}
      </h4>
      {statements.length === 0 ? (
        <p className="text-sm text-ink/60 dark:text-paper/60">{empty}</p>
      ) : (
        <ol className="flex flex-col gap-3">
          {statements.map((statement) => (
            <StatementItem key={statement.claimId} statement={statement} />
          ))}
        </ol>
      )}
    </div>
  );
}

function StatementItem({ statement }: { statement: RecapStatement }) {
  const { t } = useAppI18n();
  const copy = t.recap.sheet;
  const verdicts: Record<string, string> = t.claims.verdicts;
  const flags: Record<string, string> = t.claims.flags;
  return (
    <li className="flex flex-col gap-1.5 border-l-2 border-black/10 pl-3 dark:border-white/15 print:break-inside-avoid">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-xs">
        <span className="tabular-nums text-ink/60 dark:text-paper/60">
          {formatTime(statement.at)}
        </span>
        <span className="font-semibold">
          {verdicts[statement.verdict] ?? statement.verdict}
        </span>
        {statement.flags.length > 0 ? (
          <ul aria-label={copy.flags} className="flex flex-wrap gap-1">
            {statement.flags.map((flag) => (
              <li
                key={flag}
                className="rounded-full bg-verdict-flag/10 px-1.5 py-0.5 text-[11px] font-medium text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300"
              >
                {flags[flag] ?? flag}
              </li>
            ))}
          </ul>
        ) : null}
      </div>
      <blockquote className="text-sm">
        {truncateQuote(statement.quote, QUOTE_LIMIT)}
      </blockquote>
      {statement.rationale !== "" ? (
        <p className="text-sm text-ink/80 dark:text-paper/80">
          <span className="font-medium">{copy.justification} : </span>
          {statement.rationale}
        </p>
      ) : null}
      {statement.sources.length === 0 ? (
        <p className="text-xs text-ink/60 dark:text-paper/60">{copy.noSource}</p>
      ) : (
        <ul
          aria-label={copy.sources}
          className="flex flex-wrap gap-x-3 gap-y-1 text-xs"
        >
          {statement.sources.map((source) => (
            <li key={source.url}>
              <a
                href={source.url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-bleu underline underline-offset-2 hover:no-underline dark:text-sky-300"
              >
                {source.title}
              </a>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}
