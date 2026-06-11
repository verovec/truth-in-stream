"use client";

import type {
  ClaimMatch,
  EvidenceMatch,
  FactCheckSegment,
  SkipReason,
} from "@/lib/fact-check/api";
import { VerdictBadge } from "./verdict-badge";

// Per-segment presentation shared by the batch results list and the live
// statement list: a skip notice when the gate declined the segment, a neutral
// notice when nothing matched, or the ranked matches. The three states are
// visually and textually distinct so a skipped segment is never read as a
// verdict.

// SKIP_LABELS explains, per skip reason, why a segment was not fact-checked.
const SKIP_LABELS: Record<SkipReason, string> = {
  not_a_claim: "Not checked - no verifiable claim",
  not_covered: "Not checked - not covered by the reference corpus",
};

// skipLabel tolerates a skip reason the backend may add before the frontend
// knows it, falling back to a generic notice. The parameter is widened to
// string on purpose: the value crosses the wire unchecked.
function skipLabel(reason: string): string {
  return SKIP_LABELS[reason as SkipReason] ?? "Not checked";
}

export function SegmentDetail({ segment }: { segment: FactCheckSegment }) {
  if (segment.skipReason) {
    return (
      <p className="border-t border-dashed border-zinc-200 px-3 py-2 text-xs italic text-zinc-400 dark:border-zinc-800 dark:text-zinc-500">
        {skipLabel(segment.skipReason)}
      </p>
    );
  }

  if (segment.matches.length === 0) {
    return (
      <p className="border-t border-dashed border-zinc-200 px-3 py-2 text-xs text-zinc-500 dark:border-zinc-800 dark:text-zinc-400">
        No confident match for this segment.
      </p>
    );
  }

  return (
    <div className="flex flex-col divide-y divide-zinc-100 border-t border-zinc-200 dark:divide-zinc-900 dark:border-zinc-800">
      {segment.matches.map((match, matchIndex) =>
        match.kind === "evidence" ? (
          <EvidenceRow key={matchIndex} match={match} />
        ) : (
          <ClaimRow key={matchIndex} match={match} />
        ),
      )}
    </div>
  );
}

function SimilarityScore({ similarity }: { similarity: number }) {
  return (
    <span className="font-mono text-[11px] tabular-nums text-zinc-400 dark:text-zinc-500">
      {Math.round(similarity * 100)}% match
    </span>
  );
}

function ClaimRow({ match }: { match: ClaimMatch }) {
  return (
    <article className="flex flex-col gap-1.5 px-3 py-2">
      <div className="flex items-center justify-between gap-2">
        <VerdictBadge verdict={match.verdict} />
        <SimilarityScore similarity={match.similarity} />
      </div>
      <p className="text-sm leading-5 text-zinc-700 dark:text-zinc-300">
        {match.claim}
      </p>
      <p className="flex flex-wrap gap-x-3 gap-y-1">
        {match.sources.map((source) => (
          <a
            key={source.url}
            href={source.url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs font-medium text-sky-700 underline decoration-sky-300 underline-offset-2 hover:decoration-sky-600 dark:text-sky-400 dark:decoration-sky-700 dark:hover:decoration-sky-400"
          >
            {source.title}
          </a>
        ))}
      </p>
    </article>
  );
}

function EvidenceRow({ match }: { match: EvidenceMatch }) {
  return (
    <article className="flex flex-col gap-1.5 px-3 py-2">
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center rounded-full bg-indigo-100 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-indigo-800 dark:bg-indigo-500/15 dark:text-indigo-300">
          Evidence
        </span>
        <SimilarityScore similarity={match.similarity} />
      </div>
      <p className="text-sm leading-5 text-zinc-700 dark:text-zinc-300">
        {match.excerpt}
      </p>
      <p className="text-xs text-zinc-500 dark:text-zinc-400">
        <a
          href={match.article.url}
          target="_blank"
          rel="noopener noreferrer"
          className="font-medium text-sky-700 underline decoration-sky-300 underline-offset-2 hover:decoration-sky-600 dark:text-sky-400 dark:decoration-sky-700 dark:hover:decoration-sky-400"
        >
          {match.article.title}
        </a>
        {" · Wikipedia, "}
        <a
          href="https://creativecommons.org/licenses/by-sa/4.0/"
          target="_blank"
          rel="noopener noreferrer license"
          className="underline decoration-zinc-300 underline-offset-2 hover:decoration-zinc-500 dark:decoration-zinc-700"
        >
          CC BY-SA 4.0
        </a>
      </p>
    </article>
  );
}
