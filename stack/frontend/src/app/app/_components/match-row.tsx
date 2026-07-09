"use client";

import type { ClaimMatch, EvidenceMatch, SegmentMatch } from "@/lib/fact-check/api";
import { formatTemplate } from "@/lib/i18n/text";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { SOURCE_LINK_CLASS } from "./live-row-classes";
import { VerdictBadge } from "./verdict-badge";

// One ranked match - a curated claim verdict with its citation sources, or a
// Wikipedia evidence excerpt with attribution. Rendered by the decoupled live
// fact-check list, one row per resolved match.

// A citation link in a match row: the shared source-link treatment plus the
// row's own layout constraints so a long title wraps instead of overflowing.
const MATCH_SOURCE_LINK_CLASS = `min-w-0 break-words text-xs ${SOURCE_LINK_CLASS}`;

function SimilarityScore({ similarity }: { similarity: number }) {
  const { t } = useAppI18n();
  return (
    <span className="text-[11px] tabular-nums text-ink/40 dark:text-paper/40">
      {formatTemplate(t.legacy.similarity, {
        percent: Math.round(similarity * 100),
      })}
    </span>
  );
}

// ProvenanceDetail is the operator-only footer of a match row: the cited
// passage's stable evidence id and its stance-bearing contribution. Both ride a
// match only when DEBUG_FACT_CHECK is on, so the footer is omitted entirely for a
// normal viewer (whose matches carry neither). Operator tooling stays in
// English, like the rest of the debug surface.
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
    <p className="flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] tabular-nums text-ink/40 dark:text-paper/40">
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
      <p className="text-sm leading-6 break-words text-ink/90 dark:text-paper/90">
        {match.claim}
      </p>
      <p className="flex flex-wrap gap-x-3 gap-y-1">
        {match.sources.map((source) => (
          <a
            key={source.url}
            href={source.url}
            target="_blank"
            rel="noopener noreferrer"
            className={MATCH_SOURCE_LINK_CLASS}
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
  const { t } = useAppI18n();
  const sources = match.sources ?? [];
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center rounded-full bg-bleu/10 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-wide text-bleu dark:bg-sky-400/15 dark:text-sky-300">
          {t.legacy.evidence}
        </span>
        <SimilarityScore similarity={match.similarity} />
      </div>
      <p className="text-sm leading-6 break-words text-ink/90 dark:text-paper/90">
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
              className={MATCH_SOURCE_LINK_CLASS}
            >
              {source.title}
            </a>
          ))}
        </p>
      ) : (
        <p className="text-xs text-ink/50 dark:text-paper/50">
          <a
            href={match.article.url}
            target="_blank"
            rel="noopener noreferrer"
            className={MATCH_SOURCE_LINK_CLASS}
          >
            {match.article.title}
          </a>
          {` · ${t.legacy.wikipedia}, `}
          <a
            href="https://creativecommons.org/licenses/by-sa/4.0/"
            target="_blank"
            rel="noopener noreferrer license"
            className="underline decoration-black/20 underline-offset-2 hover:decoration-black/50 dark:decoration-white/20 dark:hover:decoration-white/50"
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
