"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import { Logo } from "@/components/marketing/logo";
import { TricoloreRule } from "@/components/marketing/tricolore-rule";
import { formatTemplate } from "@/lib/i18n/text";
import {
  addCounts,
  formatDuration,
  type Recap,
  type RecapSpeaker,
  REPORT_TIME_ZONE,
} from "@/lib/video/recap";
import { speakerDisplayName } from "@/lib/video/speaker-display-name";

// RecapCover is the first block of the report: the brand lockup, the title,
// who takes part, four key figures for the whole analysis, and its date. The
// title is a level-2 heading because the app header's brand is the page's
// level-1; the blocks that follow nest under it. The figures cover every
// voice, so they equal the analysis counters and the CSV export; the
// per-speaker table that follows covers the principals only.
export function RecapCover({
  recap,
  principals,
  brandName,
}: {
  recap: Recap;
  principals: RecapSpeaker[];
  brandName: string;
}) {
  const { locale, t } = useAppI18n();
  const copy = t.recap.cover;
  const whole = addCounts(recap.principals.counts, recap.others.counts);
  const figures = [
    { label: copy.duration, value: formatDuration(recap.duration) },
    { label: copy.detected, value: String(whole.detected) },
    { label: copy.credible, value: String(whole.credible) },
    { label: copy.disputed, value: String(whole.disputed) },
  ];
  return (
    <section
      aria-labelledby="recap-title"
      className="flex flex-col gap-5 rounded-2xl border border-black/10 bg-white p-5 sm:p-8 dark:border-white/10 dark:bg-white/5 print:break-after-page"
    >
      <div className="flex items-center gap-2.5">
        <Logo decorative size={30} />
        <span className="text-lg font-semibold tracking-tight">{brandName}</span>
      </div>
      <TricoloreRule />
      <div className="flex flex-col gap-2">
        <p className="text-xs font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.kicker}
        </p>
        <h2
          id="recap-title"
          className="font-display text-3xl font-semibold tracking-tight sm:text-4xl"
        >
          {recap.title}
        </h2>
      </div>
      <div className="flex flex-col gap-1">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.participants}
        </h3>
        {principals.length === 0 ? (
          <p className="text-sm text-ink/70 dark:text-paper/70">
            {copy.noParticipants}
          </p>
        ) : (
          <ul className="flex flex-wrap gap-x-4 gap-y-1 text-base font-medium">
            {principals.map((speaker) => (
              <li key={speaker.label}>{speakerDisplayName(speaker, t)}</li>
            ))}
          </ul>
        )}
      </div>
      <dl
        aria-label={copy.figuresAria}
        className="grid grid-cols-2 gap-3 sm:grid-cols-4"
      >
        {figures.map((figure) => (
          <div
            key={figure.label}
            className="flex flex-col-reverse gap-1 rounded-xl bg-black/[0.03] px-4 py-3 dark:bg-white/5"
          >
            <dt className="text-xs text-ink/60 dark:text-paper/60">
              {figure.label}
            </dt>
            <dd className="font-display text-2xl font-semibold tabular-nums">
              {figure.value}
            </dd>
          </div>
        ))}
      </dl>
      {recap.analyzedAt ? (
        <p className="text-xs text-ink/60 dark:text-paper/60">
          {formatTemplate(copy.analysedOn, {
            date: new Intl.DateTimeFormat(locale, {
              dateStyle: "long",
              timeZone: REPORT_TIME_ZONE,
            }).format(new Date(recap.analyzedAt)),
          })}
        </p>
      ) : null}
    </section>
  );
}
