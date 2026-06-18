import type { Verdict } from "@/lib/fact-check/api";
import type { LiteralVerdict, ManipulationFlag } from "@/lib/live/frames";

const VERDICT_STYLES: Record<Verdict, string> = {
  corroborates:
    "bg-emerald-100 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300",
  contradicts:
    "bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-300",
  unclear: "bg-amber-100 text-amber-800 dark:bg-amber-500/15 dark:text-amber-300",
};

export function VerdictBadge({ verdict }: { verdict: Verdict }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide ${VERDICT_STYLES[verdict]}`}
    >
      {verdict}
    </span>
  );
}

// LITERAL_LABELS renders the political path's face-value verdict axis in French.
// It is orthogonal to the framing flags: "Exact" means the claim is true as
// stated, independent of whether its framing is honest.
const LITERAL_LABELS: Record<LiteralVerdict, string> = {
  accurate: "Exact",
  inaccurate: "Inexact",
  unverifiable: "Invérifiable",
};

const LITERAL_STYLES: Record<LiteralVerdict, string> = {
  accurate:
    "bg-emerald-100 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300",
  inaccurate: "bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-300",
  unverifiable:
    "bg-zinc-200 text-zinc-700 dark:bg-zinc-700/40 dark:text-zinc-300",
};

// LiteralBadge shows the face-value verdict (the political path's literal axis).
// It stands apart from the credibility verdict so a viewer reads "is the claim
// true" separately from "can I trust the speaker", and apart from the flag chips
// so an accurate-but-cherry-picked claim shows both at once.
export function LiteralBadge({ literal }: { literal: LiteralVerdict }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${LITERAL_STYLES[literal]}`}
    >
      {LITERAL_LABELS[literal]}
    </span>
  );
}

// FLAG_LABELS renders the closed manipulation-flag vocabulary in French. The map
// is exhaustive over ManipulationFlag by construction: a new flag added to the
// wire enum fails to compile here until it is given a label, so a flag can never
// render as a raw slug.
const FLAG_LABELS: Record<ManipulationFlag, string> = {
  "missing-context": "Contexte manquant",
  "cherry-picked": "Données triées",
  outdated: "Périmé",
  misattributed: "Mal attribué",
  "misleading-causation": "Causalité trompeuse",
};

// FlagChips renders the manipulation flags a claim carries, orthogonal to its
// literal verdict: a literally accurate claim can still be flagged. An empty list
// renders nothing, so a flagless claim shows no chip row.
export function FlagChips({ flags }: { flags: readonly ManipulationFlag[] }) {
  if (flags.length === 0) {
    return null;
  }
  return (
    <ul aria-label="Drapeaux de manipulation" className="flex flex-wrap gap-1">
      {flags.map((flag) => (
        <li
          key={flag}
          className="inline-flex items-center rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-800 dark:bg-amber-500/15 dark:text-amber-300"
        >
          {FLAG_LABELS[flag]}
        </li>
      ))}
    </ul>
  );
}
