"use client";

import type { Verdict } from "@/lib/fact-check/api";
import type { LiteralVerdict, ManipulationFlag } from "@/lib/live/frames";
import { useAppI18n } from "@/components/i18n/app-i18n";

// The legacy evidence path's stance verdicts, tied to the shared semantic
// verdict tokens: corroborates reads credible, contradicts reads disputed,
// unclear reads as the nuance/flag tone. The wire vocabulary is untouched; only
// the label is localized.
const VERDICT_STYLES: Record<Verdict, string> = {
  corroborates:
    "bg-verdict-credible/10 text-verdict-credible dark:bg-verdict-credible/15",
  contradicts:
    "bg-verdict-disputed/10 text-verdict-disputed dark:bg-verdict-disputed/15",
  unclear:
    "bg-verdict-flag/10 text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300",
};

export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  const { t } = useAppI18n();
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide ${VERDICT_STYLES[verdict]}`}
    >
      {t.legacy.verdicts[verdict]}
    </span>
  );
}

const LITERAL_STYLES: Record<LiteralVerdict, string> = {
  accurate:
    "bg-verdict-credible/10 text-verdict-credible dark:bg-verdict-credible/15",
  inaccurate:
    "bg-verdict-disputed/10 text-verdict-disputed dark:bg-verdict-disputed/15",
  unverifiable:
    "bg-verdict-unverifiable/15 text-verdict-unverifiable dark:bg-verdict-unverifiable/15",
};

// LiteralBadge shows the face-value verdict (the political path's literal axis).
// It stands apart from the credibility verdict so a viewer reads "is the claim
// true" separately from "can I trust the speaker", and apart from the flag chips
// so an accurate-but-cherry-picked claim shows both at once.
export function LiteralBadge({ literal }: { literal: LiteralVerdict }) {
  const { t } = useAppI18n();
  return (
    <span
      className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${LITERAL_STYLES[literal]}`}
    >
      {t.claims.literal[literal]}
    </span>
  );
}

// FlagChips renders the manipulation flags a claim carries, orthogonal to its
// literal verdict: a literally accurate claim can still be flagged. An empty list
// renders nothing, so a flagless claim shows no chip row. The dictionary's flag
// map is keyed by the closed wire vocabulary, so a new flag added to the wire
// enum fails to compile here until both locales give it a label - a flag can
// never render as a raw slug.
export function FlagChips({ flags }: { flags: readonly ManipulationFlag[] }) {
  const { t } = useAppI18n();
  if (flags.length === 0) {
    return null;
  }
  const labels: Record<ManipulationFlag, string> = t.claims.flags;
  return (
    <ul aria-label={t.claims.flagsAria} className="flex flex-wrap gap-1">
      {flags.map((flag) => (
        <li
          key={flag}
          className="inline-flex items-center rounded-full bg-verdict-flag/10 px-1.5 py-0.5 text-[10px] font-medium text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300"
        >
          {labels[flag]}
        </li>
      ))}
    </ul>
  );
}
