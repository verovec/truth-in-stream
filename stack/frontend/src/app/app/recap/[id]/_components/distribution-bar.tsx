"use client";

import { useAppI18n } from "@/components/i18n/app-i18n";
import { formatTemplate } from "@/lib/i18n/text";
import type { RecapCounts } from "@/lib/video/recap";

// OUTCOMES is the fixed order and styling of the four outcomes, shared by the
// bar and its legend so the two can never drift. Not-verified is hatched as
// well as tinted: an outcome is never told apart by colour alone.
export const OUTCOMES = [
  { key: "credible", className: "bg-verdict-credible" },
  { key: "disputed", className: "bg-verdict-disputed" },
  { key: "unverifiable", className: "bg-verdict-unverifiable" },
  {
    key: "notVerified",
    className:
      "bg-[repeating-linear-gradient(135deg,var(--verdict-unverifiable)_0_2px,transparent_2px_6px)]",
  },
] as const satisfies readonly { key: keyof RecapCounts; className: string }[];

// DistributionBar shows how a set of statements broke down across the four
// outcomes. It is a single labelled image for assistive technology: the label
// states every count in words, so the bar adds a visual without withholding
// anything.
export function DistributionBar({ counts }: { counts: RecapCounts }) {
  const { t } = useAppI18n();
  const copy = t.recap.distribution;
  if (counts.detected === 0) {
    return (
      <span className="text-xs text-ink/50 dark:text-paper/50">{copy.none}</span>
    );
  }
  return (
    <div
      role="img"
      aria-label={formatTemplate(copy.aria, { ...counts })}
      className="flex h-2.5 w-full min-w-24 overflow-hidden rounded-full bg-black/5 dark:bg-white/10"
    >
      {OUTCOMES.map(({ key, className }) =>
        counts[key] > 0 ? (
          <span
            key={key}
            className={className}
            style={{ width: `${(counts[key] / counts.detected) * 100}%` }}
          />
        ) : null,
      )}
    </div>
  );
}

// DistributionLegend names the four outcomes beside a swatch of each.
export function DistributionLegend() {
  const { t } = useAppI18n();
  const copy = t.recap.distribution;
  return (
    <ul
      aria-label={copy.legend}
      className="flex flex-col gap-1 text-xs text-ink/70 sm:flex-row sm:flex-wrap sm:gap-x-5 dark:text-paper/70"
    >
      {OUTCOMES.map(({ key, className }) => (
        <li key={key} className="flex items-center gap-2">
          <span
            aria-hidden="true"
            className={`inline-block size-2.5 shrink-0 rounded-sm ${className}`}
          />
          {copy[key]}
        </li>
      ))}
    </ul>
  );
}
