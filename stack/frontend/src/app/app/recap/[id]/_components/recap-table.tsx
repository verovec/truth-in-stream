"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import {
  formatDuration,
  type Recap,
  type RecapCounts,
  type RecapSpeaker,
  unverifiedShare,
} from "@/lib/video/recap";
import { DistributionBar, DistributionLegend } from "./distribution-bar";
import { speakerDisplayName } from "./speaker-display-name";

const NUMERIC = "px-3 py-2 text-right tabular-nums";

function percent(fraction: number): number {
  return Math.round(fraction * 100);
}

// RecapTable is the second block of the report: one row per principal, a
// totals row that is the sum of those rows, the legend, and the two notes that
// keep the figures honest - how to read them and where they stop.
export function RecapTable({
  recap,
  principals,
}: {
  recap: Recap;
  principals: RecapSpeaker[];
}) {
  const { t } = useAppI18n();
  const copy = t.recap.table;
  return (
    <section
      aria-labelledby="recap-table-heading"
      className="flex flex-col gap-4 rounded-2xl border border-black/10 bg-white p-5 sm:p-8 dark:border-white/10 dark:bg-white/5 print:break-after-page"
    >
      <h3
        id="recap-table-heading"
        className="font-display text-2xl font-semibold tracking-tight"
      >
        {copy.heading}
      </h3>
      {principals.length === 0 ? (
        <p className="text-sm text-ink/70 dark:text-paper/70">{copy.empty}</p>
      ) : (
        // The table scrolls inside its own region at phone width, so the page
        // never scrolls sideways; tabIndex makes that region keyboard-reachable.
        <div
          role="region"
          aria-label={copy.caption}
          tabIndex={0}
          className="overflow-x-auto"
        >
          <table className="w-full min-w-[46rem] border-collapse text-sm">
            <caption className="sr-only">{copy.caption}</caption>
            <thead>
              <tr className="border-b border-black/10 text-xs text-ink/60 dark:border-white/10 dark:text-paper/60">
                <th scope="col" className="px-3 py-2 text-left font-medium">
                  {copy.speaker}
                </th>
                {(
                  [
                    "speakingTime",
                    "share",
                    "turns",
                    "detected",
                    "credible",
                    "disputed",
                    "unverifiable",
                    "notVerified",
                  ] as const
                ).map((column) => (
                  <th
                    key={column}
                    scope="col"
                    className="px-3 py-2 text-right font-medium"
                  >
                    {copy[column]}
                  </th>
                ))}
                <th scope="col" className="px-3 py-2 text-left font-medium">
                  {copy.distribution}
                </th>
              </tr>
            </thead>
            <tbody>
              {principals.map((speaker) => (
                <tr
                  key={speaker.label}
                  className="border-b border-black/5 dark:border-white/5"
                >
                  <th scope="row" className="px-3 py-2 text-left font-semibold">
                    {speakerDisplayName(speaker, t)}
                  </th>
                  <td className={NUMERIC}>{formatDuration(speaker.speakingTime)}</td>
                  <td className={NUMERIC}>
                    {recap.principals.speakingTime > 0
                      ? `${percent(speaker.share)} %`
                      : "–"}
                  </td>
                  <td className={NUMERIC}>{speaker.turns}</td>
                  <CountCells counts={speaker.counts} />
                  <td className="px-3 py-2">
                    <DistributionBar counts={speaker.counts} />
                  </td>
                </tr>
              ))}
            </tbody>
            <tfoot>
              <tr className="font-semibold">
                <th scope="row" className="px-3 py-2 text-left">
                  {copy.total}
                </th>
                <td className={NUMERIC}>
                  {formatDuration(recap.principals.speakingTime)}
                </td>
                <td className={NUMERIC}>
                  {recap.principals.speakingTime > 0 ? "100 %" : "–"}
                </td>
                <td className={NUMERIC}>{recap.principals.turns}</td>
                <CountCells counts={recap.principals.counts} />
                <td className="px-3 py-2">
                  <DistributionBar counts={recap.principals.counts} />
                </td>
              </tr>
            </tfoot>
          </table>
        </div>
      )}
      <DistributionLegend />
      <RecapNotes recap={recap} />
    </section>
  );
}

function CountCells({ counts }: { counts: RecapCounts }) {
  return (
    <>
      <td className={NUMERIC}>{counts.detected}</td>
      <td className={NUMERIC}>{counts.credible}</td>
      <td className={NUMERIC}>{counts.disputed}</td>
      <td className={NUMERIC}>{counts.unverifiable}</td>
      <td className={NUMERIC}>{counts.notVerified}</td>
    </>
  );
}

// RecapNotes states, in plain words, how to read the figures and what they do
// not establish: how many statements carry no usable verdict, that voices are
// attributed automatically, and that small counts do not rank anyone. The
// voices outside the sheets - moderators, guests - are listed here with their
// speaking time.
function RecapNotes({ recap }: { recap: Recap }) {
  const { t } = useAppI18n();
  const copy = t.recap.notes;
  const counts = recap.principals.counts;
  // Speech the transcriber attributed to nobody (an empty label) is not a
  // voice that could have been named, so it stays out of the attribution count.
  const voices = recap.speakers.filter((speaker) => speaker.label !== "");
  const named = voices.filter((speaker) => speaker.name !== "");
  const confirmed = named.filter((speaker) => speaker.nameConfirmed).length;
  const others = recap.speakers.filter((speaker) => speaker.role !== "principal");
  return (
    <div className="grid gap-4 text-sm text-ink/80 sm:grid-cols-2 dark:text-paper/80">
      <div className="flex flex-col gap-1">
        <h4 className="text-xs font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.readingHeading}
        </h4>
        <p>{copy.reading}</p>
        {others.length > 0 ? (
          <>
            <h4 className="mt-2 text-xs font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
              {copy.othersHeading}
            </h4>
            <ul className="flex flex-col gap-0.5">
              {others.map((speaker) => (
                <li key={speaker.label}>
                  {formatTemplate(copy.other, {
                    name: speakerDisplayName(speaker, t),
                    role: t.speakerNaming.roles[speaker.role],
                    time: formatDuration(speaker.speakingTime),
                    detected: speaker.counts.detected,
                  })}
                </li>
              ))}
            </ul>
          </>
        ) : null}
      </div>
      <div className="flex flex-col gap-1">
        <h4 className="text-xs font-semibold uppercase tracking-wide text-ink/60 dark:text-paper/60">
          {copy.limitsHeading}
        </h4>
        <ul className="flex list-disc flex-col gap-1 pl-4">
          <li>
            {counts.detected === 0
              ? copy.noStatements
              : formatTemplate(copy.unverified, {
                  pct: percent(unverifiedShare(counts)),
                  count: counts.unverifiable + counts.notVerified,
                  total: counts.detected,
                })}
          </li>
          <li>
            {formatTemplate(copy.attribution, {
              automatic: named.length - confirmed,
              confirmed,
              unnamed: voices.length - named.length,
            })}
          </li>
          <li>{copy.ranking}</li>
        </ul>
      </div>
    </div>
  );
}
