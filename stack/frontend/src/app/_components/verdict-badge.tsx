import type { Verdict } from "@/lib/fact-check/api";

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
