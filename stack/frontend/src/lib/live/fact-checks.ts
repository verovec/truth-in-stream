// Derives the flat fact-check list shown below the subtitles from the same live
// state that drives the transcript, so the two can never disagree. Each verdict
// or piece of evidence becomes its own entry that points back at the statement
// it came from. Two paths feed it: the legacy curated path scores a statement
// and contributes one entry per match; the retrieve-then-verify path fans a
// statement into atomic claims and contributes one entry per verified claim,
// since a verify-path statement is never resolved at the statement level.
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveClaim } from "./claims";
import type { ClaimVerdict } from "./frames";
import { isScored, type LiveStatement } from "./statements";

// FactCheckEntry is one resolved verdict, decoupled from its statement row but
// still referencing it (statementId for selection, start/snippet so the link to
// the subtitle stays legible). The kind discriminates the two sources: a curated
// "match" carries a SegmentMatch; a verify-path "claim" carries a LiveClaim with
// its own verdict, source, and reasoning.
export type FactCheckEntry =
  | {
      kind: "match";
      key: string;
      statementId: string;
      start: number;
      snippet: string;
      match: SegmentMatch;
    }
  | {
      kind: "claim";
      key: string;
      statementId: string;
      start: number;
      snippet: string;
      claim: LiveClaim;
    };

// deriveFactChecks flattens resolved verdicts into per-entry rows, in the
// statements' existing start order. The section shows only concrete results: a
// verify-path claim verdict of credible or disputed, or a curated match that
// took a stance (corroborates or contradicts). An unverifiable claim, an
// "unclear" curated match, and a verdict-less evidence match are all dropped
// here - they remain visible only in the inline per-statement view, which this
// derivation does not touch.
//
// On the verify path a statement fans into atomic claims (read through
// claimsFor); each verified claim with a concrete verdict becomes an entry, and
// the statement's own status - which the verify path never resolves past
// "analysing" - is ignored. A statement with no claims falls back to the legacy
// path: the isScored guard keeps a skipped or errored statement out of the list
// even if it somehow arrives with matches, so a row the subtitle marks "Not
// checked" can never also show a verdict here. Claims that are still pending,
// shed to capacity, or errored contribute nothing: they carry no verdict and
// stay visible only as the inline per-claim status.
export function deriveFactChecks(
  statements: readonly LiveStatement[],
  claimsFor?: (statementId: string) => LiveClaim[],
): FactCheckEntry[] {
  const entries: FactCheckEntry[] = [];
  for (const statement of statements) {
    const claims = claimsFor?.(statement.id) ?? [];
    if (claims.length > 0) {
      for (const claim of claims) {
        if (claim.status !== "verified" || !isConcreteClaimVerdict(claim.verdict)) {
          continue;
        }
        entries.push({
          kind: "claim",
          key: `${statement.id}:claim:${claim.claimId}`,
          statementId: statement.id,
          start: statement.start,
          snippet: claim.text || statement.text,
          claim,
        });
      }
      continue;
    }
    if (!isScored(statement)) {
      continue;
    }
    statement.matches.forEach((match, index) => {
      if (!isConcreteMatch(match)) {
        return;
      }
      entries.push({
        kind: "match",
        key: `${statement.id}:${index}`,
        statementId: statement.id,
        start: statement.start,
        snippet: statement.text,
        match,
      });
    });
  }
  return entries;
}

// isConcreteClaimVerdict gates the verify path: only a credible or disputed
// verdict is a concrete result. An unverifiable verdict, or a verified frame
// that arrived with no verdict at all, carries no concrete result and is left to
// the inline per-claim view.
function isConcreteClaimVerdict(verdict: ClaimVerdict | undefined): boolean {
  return verdict === "credible" || verdict === "disputed";
}

// isConcreteMatch gates the legacy curated path with the same intent: only a
// claim match that took a stance (corroborates or contradicts) is a concrete
// result. An "unclear" claim match is the curated unverifiable-equivalent, and
// an evidence match carries no verdict at all (it is supporting context); both
// are dropped from the section.
function isConcreteMatch(match: SegmentMatch): boolean {
  return (
    match.kind === "claim" &&
    (match.verdict === "corroborates" || match.verdict === "contradicts")
  );
}
