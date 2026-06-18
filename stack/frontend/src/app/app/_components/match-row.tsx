"use client";

import type { ClaimMatch, EvidenceMatch, SegmentMatch } from "@/lib/fact-check/api";
import { VerdictBadge } from "./verdict-badge";

// One ranked match - a curated claim verdict with its citation sources, or a
// Wikipedia evidence excerpt with attribution. Rendered by the decoupled live
// fact-check list, one row per resolved match.

function SimilarityScore({ similarity }: { similarity: number }) {
  return (
    <span className="font-mono text-[11px] tabular-nums text-zinc-400 dark:text-zinc-500">
      {Math.round(similarity * 100)}% match
    </span>
  );
}

// ProvenanceDetail is the operator-only footer of a match row: the cited
// passage's stable evidence id and its stance-bearing contribution. Both ride a
// match only when DEBUG_FACT_CHECK is on, so the footer is omitted entirely for a
// normal viewer (whose matches carry neither).
function ProvenanceDetail({
  evidenceId,
  contribution,
}: {
  evidenceId?: string;
  contribution?: number;
}) {
  if (!evidenceId && contribution === undefined) {
    return null;
  }
  return (
    <p className="flex flex-wrap gap-x-3 gap-y-0.5 font-mono text-[10px] tabular-nums text-zinc-400 dark:text-zinc-500">
      {evidenceId ? <span>{evidenceId}</span> : null}
      {contribution !== undefined ? (
        <span>contribution {contribution.toFixed(2)}</span>
      ) : null}
    </p>
  );
}

function ClaimRow({ match }: { match: ClaimMatch }) {
  return (
    <div className="flex flex-col gap-1.5">
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
      <ProvenanceDetail
        evidenceId={match.evidenceId}
        contribution={match.contribution}
      />
    </div>
  );
}

function EvidenceRow({ match }: { match: EvidenceMatch }) {
  const sources = match.sources ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center rounded-full bg-indigo-100 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-indigo-800 dark:bg-indigo-500/15 dark:text-indigo-300">
          Evidence
        </span>
        <SimilarityScore similarity={match.similarity} />
      </div>
      <p className="text-sm leading-5 text-zinc-700 dark:text-zinc-300">
        {match.excerpt}
      </p>
      {sources.length > 0 ? (
        // A routed source-pack passage (INSEE, voting record, press): credit its
        // real publisher, not the generic Wikipedia attribution.
        <p className="flex flex-wrap gap-x-3 gap-y-1">
          {sources.map((source) => (
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
      ) : (
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
      )}
      <ProvenanceDetail
        evidenceId={match.evidenceId}
        contribution={match.contribution}
      />
    </div>
  );
}

export function MatchRow({ match }: { match: SegmentMatch }) {
  return match.kind === "evidence" ? (
    <EvidenceRow match={match} />
  ) : (
    <ClaimRow match={match} />
  );
}
