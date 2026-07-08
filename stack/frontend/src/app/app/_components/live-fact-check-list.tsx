"use client";

import { memo } from "react";
import type { FactCheckEntry } from "@/lib/live/fact-checks";
import { formatTime } from "@/lib/playback/format-time";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { VerifiedClaim } from "./live-claim-verdict";
import {
  LIVE_ROW_BASE_CLASS,
  LIVE_ROW_EMPHASIZED_CLASS,
} from "./live-row-classes";
import { MatchRow } from "./match-row";

// LiveFactCheckList is the decoupled fact-check region below the subtitles: a
// flat list of resolved verdicts and evidence, each referencing the statement it
// came from. Selecting an entry hands its statement id up so the subtitle region
// can highlight and scroll the origin into view. Memoized so an interim caption
// update of the parent panel does not re-render it.
export const LiveFactCheckList = memo(function LiveFactCheckList({
  entries,
  selectedStatementId,
  onSelect,
}: {
  entries: FactCheckEntry[];
  selectedStatementId: string | null;
  onSelect: (statementId: string) => void;
}) {
  const { t } = useAppI18n();
  if (entries.length === 0) {
    return (
      <p className="text-sm text-ink/60 dark:text-paper/60">
        {t.factChecks.empty}
      </p>
    );
  }

  return (
    <ol
      aria-label={t.factChecks.resultsAria}
      className="-mr-2 flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-2"
    >
      {entries.map((entry) => {
        const selected = entry.statementId === selectedStatementId;
        return (
          <li
            key={entry.key}
            aria-current={selected ? "true" : undefined}
            className={`rounded-lg border transition-colors ${
              selected ? LIVE_ROW_EMPHASIZED_CLASS : LIVE_ROW_BASE_CLASS
            }`}
          >
            <button
              type="button"
              onClick={() => onSelect(entry.statementId)}
              className="flex w-full items-baseline gap-2 rounded-t-lg px-3 py-1.5 text-left hover:bg-ink/5 focus-visible:outline-2 focus-visible:outline-bleu-flag dark:hover:bg-white/5 dark:focus-visible:outline-paper/60"
            >
              <span className="font-mono text-[11px] tabular-nums text-ink/50 dark:text-paper/50">
                {formatTime(entry.start)}
              </span>
              <span className="line-clamp-1 min-w-0 flex-1 text-xs italic text-ink/50 dark:text-paper/50">
                {entry.snippet}
              </span>
            </button>
            <div className="border-t border-black/10 px-3 py-2 dark:border-white/10">
              {entry.kind === "match" ? (
                <MatchRow match={entry.match} />
              ) : (
                <VerifiedClaim claim={entry.claim} />
              )}
            </div>
          </li>
        );
      })}
    </ol>
  );
});
