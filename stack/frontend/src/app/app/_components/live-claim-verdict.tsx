"use client";

import { useState } from "react";
import type { SegmentMatch } from "@/lib/fact-check/api";
import { formatTemplate } from "@/lib/i18n/text";
import type { LiveClaim } from "@/lib/live/claims";
import type { ClaimVerdict, VerdictSource } from "@/lib/live/frames";
import { useAppI18n } from "@/components/i18n/app-i18n";
import { SOURCE_LINK_CLASS } from "./live-row-classes";
import { MatchRow } from "./match-row";
import { FlagChips, LiteralBadge } from "./verdict-badge";

// The verify path's credibility verdicts on the shared semantic verdict tokens.
// unverifiable is a first-class verdict, shown as its own label rather than an
// error or an empty row; labels come from the active locale's dictionary.
const VERDICT_STYLES: Record<ClaimVerdict, string> = {
  credible:
    "bg-verdict-credible/10 text-verdict-credible dark:bg-verdict-credible/15",
  disputed:
    "bg-verdict-disputed/10 text-verdict-disputed dark:bg-verdict-disputed/15",
  unverifiable:
    "bg-verdict-unverifiable/15 text-verdict-unverifiable dark:bg-verdict-unverifiable/15",
};

// Source chips distinguish a verdict borrowed from a curated near-match
// (instant, no model) from one the evidence verifier reasoned out, so the viewer
// can weigh a borrowed verdict against a reasoned one: curated leans on the
// brand bleu, verified stays a neutral bordered chip.
const SOURCE_STYLES: Record<VerdictSource, string> = {
  curated: "bg-bleu/10 text-bleu dark:bg-sky-400/15 dark:text-sky-300",
  verified:
    "border border-black/10 bg-ink/5 text-ink/70 dark:border-white/10 dark:bg-white/10 dark:text-paper/70",
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
      // A routed source-pack passage carries its real publisher in sources; only
      // a Wikipedia passage falls back to the article attribution.
      const source = match.sources?.[0];
      if (source) {
        return { title: source.title, url: source.url, span: match.excerpt };
      }
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
  const { t } = useAppI18n();
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
            {t.claims.verdicts[verdict]}
          </span>
        ) : null}
        {claim.basis === "knowledge" ? (
          // A knowledge-basis verdict rests on the model's general knowledge, not a
          // retrieved passage, so it is marked as having no direct sources and the
          // viewer can weigh it as lower-confidence.
          <span className="inline-flex items-center rounded-full bg-verdict-flag/10 px-1.5 py-0.5 text-[10px] font-medium text-verdict-flag dark:bg-verdict-flag/15 dark:text-amber-300">
            {t.claims.noDirectSource}
          </span>
        ) : null}
        {claim.source ? (
          <span
            className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ${SOURCE_STYLES[claim.source]}`}
          >
            {t.claims.sources[claim.source]}
          </span>
        ) : null}
        {claim.sourceLabel ? (
          <SourceLabelChip label={claim.sourceLabel} url={claim.sourceUrl} />
        ) : null}
        {typeof claim.confidence === "number" ? (
          <span className="text-[10px] tabular-nums text-ink/40 dark:text-paper/40">
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
          className="self-start text-[11px] font-medium text-bleu underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-bleu-flag dark:text-sky-300 dark:focus-visible:outline-paper/60"
        >
          {expanded ? t.claims.hideDetail : t.claims.showDetail}
        </button>
      ) : null}
      {expanded ? (
        <div className="flex flex-col gap-2 pt-1">
          {claim.rationale ? (
            <p className="text-[11px] leading-5 text-ink/60 dark:text-paper/60">
              {claim.rationale}
            </p>
          ) : null}
          {matches.map((match, index) => (
            <div
              key={`${claim.claimId}:${index}`}
              className="rounded-md border border-black/10 px-2 py-1.5 dark:border-white/10"
            >
              <MatchRow match={match} />
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

// SourceLabelChip names the authoritative provider that backed the verdict
// (INSEE, Wikipédia, Assemblée nationale, ...), the clean provenance a normal
// viewer sees without the operator detail. It reads "Source : <provider>" so the
// provenance is spelled out rather than left as a bare name; it links the
// provider when a url is present and is otherwise a plain chip. It is rendered
// only when a label exists, so a knowledge-only verdict shows no empty chip.
function SourceLabelChip({ label, url }: { label: string; url?: string }) {
  const { t } = useAppI18n();
  const base =
    "inline-flex items-center rounded-full bg-ink/5 px-1.5 py-0.5 text-[10px] font-medium text-ink/70 dark:bg-white/10 dark:text-paper/70";
  const text = formatTemplate(t.claims.sourcePrefix, { label });
  if (url) {
    return (
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        className={`${base} underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-bleu-flag dark:focus-visible:outline-paper/60`}
      >
        {text}
      </a>
    );
  }
  return <span className={base}>{text}</span>;
}

// PrimarySourceRow surfaces the leading citation - the source name as a link and
// the exact quoted span - so a viewer can weigh the grounding at a glance, before
// (and independently of) expanding the full citation list. The span is quoted so
// it reads as the cited words, not the UI's own claim.
function PrimarySourceRow({ source }: { source: PrimarySource }) {
  const { t } = useAppI18n();
  return (
    <p
      aria-label={t.claims.primarySourceAria}
      className="text-[11px] leading-5 text-ink/60 dark:text-paper/60"
    >
      <a
        href={source.url}
        target="_blank"
        rel="noopener noreferrer"
        className={SOURCE_LINK_CLASS}
      >
        {source.title}
      </a>
      {" — « "}
      {source.span}
      {" »"}
    </p>
  );
}
