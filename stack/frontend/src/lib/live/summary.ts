// Derives the running findings summary shown in the top-of-page strip from the
// same live statement state that drives the subtitles and the fact-check list,
// so the three can never disagree. It is a pure projection over the already
// id-deduped, ordered statement list, so a replay after a reconnect cannot be
// double-counted: the statements reducer collapses it before it reaches here.
import { isScored, type LiveStatement } from "./statements";

// LiveSummary is the at-a-glance tally for one analysis session. Every statement
// falls into exactly one of checked / skipped / analysing; the verdict and
// evidence counts break the checked bucket down by what its matches found.
export type LiveSummary = {
  // Statements that were fact-checked: resolved to "checked" with no skip and no
  // error. Some may have found no match; they still count as checked.
  checked: number;
  // Claim-verdict tallies across the matches of every checked statement. These
  // mirror the claim entries in the fact-check list one-for-one.
  corroborates: number;
  contradicts: number;
  unclear: number;
  // Supporting Wikipedia evidence matches across checked statements, kept
  // distinct from claim verdicts.
  evidence: number;
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
    evidence: 0,
    skipped: 0,
    analysing: 0,
  };
}

/**
 * Folds the live statements into a LiveSummary. Verdicts and evidence are
 * tallied only from scored statements (see isScored), the same rule the
 * fact-check list uses, so a row the list marks "not checked" can never inflate
 * a verdict count here. A checked-but-skipped or errored statement counts as
 * not-checked even if it carries matches; a statement still analysing is
 * in-progress.
 */
export function summarizeStatements(
  statements: readonly LiveStatement[],
): LiveSummary {
  const summary = emptySummary();
  for (const statement of statements) {
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
