// Derives the flat fact-check list shown below the subtitles from the same live
// statement state that drives the transcript, so the two can never disagree.
// Each verdict or piece of evidence becomes its own entry that points back at
// the statement it came from.
import type { SegmentMatch } from "@/lib/fact-check/api";
import type { LiveStatement } from "./statements";

// FactCheckEntry is one resolved claim or evidence match, decoupled from its
// statement row but still referencing it (id for selection, start/snippet so the
// link to the subtitle stays legible).
export type FactCheckEntry = {
  key: string;
  statementId: string;
  start: number;
  snippet: string;
  match: SegmentMatch;
};

// deriveFactChecks flattens checked statements into per-match entries, in the
// statements' existing start order. Analysing, errored, skipped, and no-match
// statements contribute nothing: they carry no verdict and stay visible only as
// subtitles. The skipReason guard keeps a skipped statement out of the
// fact-check list even if it somehow arrives with matches, so a row the subtitle
// marks "Not checked" can never also show a verdict here.
export function deriveFactChecks(
  statements: readonly LiveStatement[],
): FactCheckEntry[] {
  const entries: FactCheckEntry[] = [];
  for (const statement of statements) {
    if (
      statement.status !== "checked" ||
      statement.error ||
      statement.skipReason
    ) {
      continue;
    }
    statement.matches.forEach((match, index) => {
      entries.push({
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
