// Derives the running findings summary shown in the top-of-page strip from the
// same live statement and claim state that drives the subtitles and the
// fact-check list, so the three can never disagree. It is a pure projection over
// the already id-deduped, ordered statement list, so a replay after a reconnect
// cannot be double-counted: the statements reducer collapses it before it
// reaches here.
import {
  type ClaimsState,
  claimsForUnit,
  isTerminalClaimStatus,
  type LiveClaim,
} from "./claims";
import type { ClaimVerdict } from "./frames";
import { isScored, type LiveStatement } from "./statements";

// LiveSummary is the at-a-glance tally for one analysis session. Every statement
// falls into exactly one of checked / skipped / analysing; the verdict and
// evidence counts break the checked bucket down by what its matches found.
export type LiveSummary = {
  // Statements that were fact-checked: resolved to "checked" with no skip and no
  // error. Some may have found no match; they still count as checked.
  checked: number;
  // Claim-verdict tallies across the matches of every checked statement. These
  // mirror the claim entries in the fact-check list one-for-one. unclear is the
  // legacy curated path's neutral verdict; unverifiable is the verify path's
  // first-class "can't be confirmed" verdict, kept distinct so the strip reads
  // the same word as the per-claim list.
  corroborates: number;
  contradicts: number;
  unclear: number;
  unverifiable: number;
  // Supporting Wikipedia evidence matches across checked statements, kept
  // distinct from claim verdicts.
  evidence: number;
  // Verified claims (verify path) that carried at least one manipulation flag,
  // orthogonal to the verdict counts: a claim can be literally accurate yet
  // cherry-picked, so a flagged claim is tallied here in addition to its verdict.
  // It stays zero on the legacy and credibility-only paths, which never flag.
  misleadingFraming: number;
  // Statements transcribed but never given a verdict: skipped by the
  // check-worthiness gate, left unscored under load, or errored.
  skipped: number;
  // Statements still being analysed, with no verdict yet.
  analysing: number;
};

export function emptySummary(): LiveSummary {
  return {
    checked: 0,
    corroborates: 0,
    contradicts: 0,
    unclear: 0,
    unverifiable: 0,
    evidence: 0,
    misleadingFraming: 0,
    skipped: 0,
    analysing: 0,
  };
}

/**
 * Folds the live statements into a LiveSummary. On the retrieve-then-verify
 * path a unit fans into atomic claims, each carrying its own verdict, so when a
 * statement has claims the summary derives from those claims (the same entries
 * the fact-check list renders) rather than the statement's own status, which the
 * verify path never resolves. A legacy statement with no claims is tallied from
 * its status and matches exactly as before.
 *
 * Verdicts and evidence are tallied only from scored statements (see isScored)
 * or verified claims, the same rule the fact-check list uses, so a row the list
 * marks "not checked" can never inflate a verdict count here. A checked-but-
 * skipped or errored statement counts as not-checked even if it carries matches;
 * a statement still analysing is in-progress.
 */
export function summarizeStatements(
  statements: readonly LiveStatement[],
  claims?: ClaimsState,
): LiveSummary {
  const summary = emptySummary();
  for (const statement of statements) {
    const unitClaims = claims ? claimsForUnit(claims, statement.id) : [];
    if (unitClaims.length > 0) {
      tallyClaimUnit(summary, unitClaims);
      continue;
    }
    if (statement.status === "analysing") {
      summary.analysing += 1;
      continue;
    }
    if (!isScored(statement)) {
      summary.skipped += 1;
      continue;
    }
    summary.checked += 1;
    for (const match of statement.matches) {
      if (match.kind === "evidence") {
        summary.evidence += 1;
      } else {
        summary[match.verdict] += 1;
      }
    }
  }
  return summary;
}

// VERIFY_VERDICT_BUCKET maps a verified claim's credibility verdict (the
// verifier's credible/disputed/unverifiable vocabulary) onto the strip's counts.
// credible and disputed reuse the curated corroborates/contradicts buckets, but
// unverifiable keeps its own bucket rather than collapsing into the curated
// unclear, so the strip reads "Unverifiable" exactly as the per-claim list does.
// A verified claim with no verdict (a degenerate frame) reads as unverifiable,
// mirroring how the list renders it. The Record over ClaimVerdict is exhaustive by
// construction: a new verdict added to the wire enum fails to compile here until it
// is given a bucket, rather than silently falling through to a wrong count.
const VERIFY_VERDICT_BUCKET: Record<
  ClaimVerdict,
  "corroborates" | "contradicts" | "unverifiable"
> = {
  credible: "corroborates",
  disputed: "contradicts",
  unverifiable: "unverifiable",
};

// tallyClaimUnit folds one verify-path unit's claims into the summary. The unit
// is in progress until every claim is terminal; once resolved it counts as
// checked when at least one claim reached a verdict, otherwise not-checked (every
// claim was shed to capacity or errored). Verified claims contribute their
// verdict to the matching count; non-verified terminal claims contribute none,
// the same way the list shows them as a status rather than a verdict.
function tallyClaimUnit(summary: LiveSummary, unitClaims: LiveClaim[]): void {
  if (!unitClaims.every((claim) => isTerminalClaimStatus(claim.status))) {
    summary.analysing += 1;
    return;
  }
  if (unitClaims.some((claim) => claim.status === "verified")) {
    summary.checked += 1;
  } else {
    summary.skipped += 1;
  }
  for (const claim of unitClaims) {
    if (claim.status !== "verified") {
      continue;
    }
    summary[VERIFY_VERDICT_BUCKET[claim.verdict ?? "unverifiable"]] += 1;
    // A flagged claim is counted on the orthogonal misleading-framing axis in
    // addition to its verdict bucket: literally accurate yet cherry-picked still
    // moves this tally, so the strip can surface dishonest framing apart from
    // outright falsehood.
    if ((claim.flags?.length ?? 0) > 0) {
      summary.misleadingFraming += 1;
    }
  }
}
