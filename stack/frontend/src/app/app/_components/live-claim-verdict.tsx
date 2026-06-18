"use client";

import { useState } from "react";
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveClaim } from "@/lib/live/claims";
import type { ClaimVerdict, VerdictSource } from "@/lib/live/frames";
import { MatchRow } from "./match-row";
import { FlagChips, LiteralBadge } from "./verdict-badge";

// VERDICT_LABELS renders the verify path's credibility verdict enum in French.
// unverifiable is a first-class verdict, shown as "Invérifiable" rather than an
// error or an empty row.
const VERDICT_LABELS: Record<ClaimVerdict, string> = {
  credible: "Fiable",
  disputed: "Contesté",
  unverifiable: "Invérifiable",
};

const VERDICT_STYLES: Record<ClaimVerdict, string> = {
  credible:
    "bg-emerald-100 text-emerald-800 dark:bg-emerald-500/15 dark:text-emerald-300",
  disputed: "bg-rose-100 text-rose-800 dark:bg-rose-500/15 dark:text-rose-300",
  unverifiable:
    "bg-zinc-200 text-zinc-700 dark:bg-zinc-700/40 dark:text-zinc-300",
};

// SOURCE_LABELS distinguishes a verdict borrowed from a curated near-match
// (instant, no model) from one the evidence verifier reasoned out, so the viewer
// can weigh a borrowed verdict against a reasoned one.
const SOURCE_LABELS: Record<VerdictSource, string> = {
  curated: "source vérifiée",
  verified: "vérifié sur preuves",
};

const SOURCE_STYLES: Record<VerdictSource, string> = {
  curated:
    "bg-violet-100 text-violet-700 dark:bg-violet-500/15 dark:text-violet-300",
  verified: "bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300",
};

// PrimarySource is the name, url, and quoted span of one cited passage, the
// least-derived view of where a verdict's grounding comes from. A curated/claim
// match carries its citation sources and the matched claim text as the span; a
// Wikipedia evidence match carries its article attribution and the excerpt.
type PrimarySource = { title: string; url: string; span: string };

// primarySourceOf pulls the leading cited passage's provenance to the surface so a
// viewer sees the strongest source without expanding the reasoning. A named claim
// source (curated/primary, the design's preferred attribution) outranks a
// Wikipedia evidence fallback regardless of array position, so a claim match with
// sources wins even when an evidence match precedes it. It returns null when
// nothing is cited (a knowledge-basis verdict) so the affordance is omitted rather
// than rendering an empty shell.
function primarySourceOf(matches: readonly SegmentMatch[]): PrimarySource | null {
  for (const match of matches) {
    if (match.kind === "claim") {
      const source = match.sources[0];
      if (source) {
        return { title: source.title, url: source.url, span: match.claim };
      }
    }
  }
  for (const match of matches) {
    if (match.kind === "evidence") {
      return {
        title: match.article.title,
        url: match.article.url,
        span: match.excerpt,
      };
    }
  }
  return null;
}

// VerifiedClaim is the verdict view for a resolved claim: the literal verdict
// badge and any manipulation-flag chips (the political two-axis display), the
// credibility verdict, the source tag and confidence, the primary source surfaced
// inline, and the full citations and rationale revealed on tap. It is shared by
// the inline per-statement claim list and the decoupled fact-check section so the
// two render one claim identically. A degenerate verified frame with no verdict
// (defensive) reads as invérifiable rather than a blank row.
export function VerifiedClaim({ claim }: { claim: LiveClaim }) {
  const [expanded, setExpanded] = useState(false);
  const verdict = claim.verdict ?? "unverifiable";
  const flags = claim.flags ?? [];
  const matches = claim.matches ?? [];
  const primary = primarySourceOf(matches);
  const hasDetail = matches.length > 0 || Boolean(claim.rationale);
  // On the political path the literal axis and the credibility verdict both read
  // "Invérifiable" when nothing settles the claim - the credibility verdict is
  // derived from the literal one - so showing both stacks two identical badges.
  // Suppress the credibility badge only in that collinear case; for accurate or
  // inaccurate the two axes carry distinct labels (truth vs trust) and both show.
  const showVerdict = !(claim.literal === "unverifiable" && verdict === "unverifiable");

  return (
    <div className="mt-1 flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        {claim.literal ? <LiteralBadge literal={claim.literal} /> : null}
        {showVerdict ? (
          <span
            className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${VERDICT_STYLES[verdict]}`}
          >
            {VERDICT_LABELS[verdict]}
          </span>
        ) : null}
        {claim.basis === "knowledge" ? (
          // A knowledge-basis verdict rests on the model's general knowledge, not a
          // retrieved passage, so it is marked as having no direct sources and the
          // viewer can weigh it as lower-confidence.
          <span className="inline-flex items-center rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-500/15 dark:text-amber-300">
            sans source directe
          </span>
        ) : null}
        {claim.source ? (
          <span
            className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ${SOURCE_STYLES[claim.source]}`}
          >
            {SOURCE_LABELS[claim.source]}
          </span>
        ) : null}
        {typeof claim.confidence === "number" ? (
          <span className="font-mono text-[10px] tabular-nums text-zinc-400 dark:text-zinc-500">
            {Math.round(claim.confidence * 100)}%
          </span>
        ) : null}
      </div>
      <FlagChips flags={flags} />
      {primary ? <PrimarySourceRow source={primary} /> : null}
      {hasDetail ? (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((prev) => !prev)}
          className="self-start text-[11px] font-medium text-sky-700 underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-sky-500 dark:text-sky-400"
        >
          {expanded ? "Masquer le détail" : "Voir le détail"}
        </button>
      ) : null}
      {expanded ? (
        <div className="flex flex-col gap-2 pt-1">
          {claim.rationale ? (
            <p className="text-[11px] leading-5 text-zinc-600 dark:text-zinc-400">
              {claim.rationale}
            </p>
          ) : null}
          {matches.map((match, index) => (
            <div
              key={`${claim.claimId}:${index}`}
              className="rounded-md border border-zinc-200 px-2 py-1.5 dark:border-zinc-800"
            >
              <MatchRow match={match} />
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

// PrimarySourceRow surfaces the leading citation - the source name as a link and
// the exact quoted span - so a viewer can weigh the grounding at a glance, before
// (and independently of) expanding the full citation list. The span is quoted so
// it reads as the cited words, not the UI's own claim.
function PrimarySourceRow({ source }: { source: PrimarySource }) {
  return (
    <p
      aria-label="Source principale"
      className="text-[11px] leading-5 text-zinc-600 dark:text-zinc-400"
    >
      <a
        href={source.url}
        target="_blank"
        rel="noopener noreferrer"
        className="font-medium text-sky-700 underline decoration-sky-300 underline-offset-2 hover:decoration-sky-600 dark:text-sky-400 dark:decoration-sky-700 dark:hover:decoration-sky-400"
      >
        {source.title}
      </a>
      {" — « "}
      {source.span}
      {" »"}
    </p>
  );
}
