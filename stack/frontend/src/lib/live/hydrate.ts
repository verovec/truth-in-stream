// Hydrates a stored pre-analysis into the same shape a live session produces,
// by folding the stored frames through the exact reducers the live hook uses.
// Stored frames carry absolute video-time timestamps (the headless job streams
// from position 0), so no base-time shift and no session id-namespacing is
// applied: ids are already consistent within the one stored session. Interim
// captions are transient by definition and are skipped, exactly as a live
// viewer never sees them again after the utterance commits.
import type { LiveAnalysis } from "@/hooks/use-live-analysis";
import {
  applyClaimResultFrame,
  applyClaimsFrame,
  type ClaimsState,
  claimsForUnit,
  emptyClaims,
} from "./claims";
import type { LiveFrame } from "./frames";
import { claimHighlights, NO_HIGHLIGHTS } from "./highlight";
import { mergeUnitStatements } from "./merge";
import {
  applySpeakerTallyFrame,
  emptySpeakers,
  listSpeakers,
  type SpeakersState,
} from "./speakers";
import {
  applyFrame,
  emptyStatements,
  listStatements,
  type StatementsState,
} from "./statements";
import { summarizeStatements } from "./summary";

export type HydratedAnalysisState = {
  statements: StatementsState;
  claims: ClaimsState;
  speakers: SpeakersState;
};

/**
 * Folds a stored frame list into the live stores. Pure: reducing the same
 * frames always yields the same state, so hydration is refresh-safe and
 * order-tolerant exactly as far as the live reducers are.
 */
export function foldAnalysisFrames(
  frames: readonly LiveFrame[],
): HydratedAnalysisState {
  let statements = emptyStatements();
  let claims = emptyClaims();
  let speakers = emptySpeakers();
  for (const frame of frames) {
    switch (frame.type) {
      case "interim":
        break;
      case "speaker_tally":
        speakers = applySpeakerTallyFrame(speakers, frame);
        break;
      case "claims":
        claims = applyClaimsFrame(claims, frame);
        break;
      case "claim_result":
        claims = applyClaimResultFrame(claims, frame);
        break;
      default:
        statements = applyFrame(statements, frame);
    }
  }
  return { statements, claims, speakers };
}

/**
 * Projects the hydrated state into the snapshot shape the live components
 * read. status is "ended": the stored session is finished, so the panel shows
 * no live pill, no reconnect notice, and the summary strip shows the tally
 * without a connection indicator. The caption is empty - there is no utterance
 * in flight in a stored session.
 */
export function analysedSnapshot(state: HydratedAnalysisState): LiveAnalysis {
  const statements = mergeUnitStatements(
    listStatements(state.statements),
    state.claims.members,
  );
  const highlightIndex = claimHighlights(state.claims);
  return {
    statements,
    caption: "",
    status: "ended",
    summary: summarizeStatements(statements, state.claims),
    claimsFor: (statementId) => claimsForUnit(state.claims, statementId),
    highlightsFor: (segmentId) =>
      highlightIndex.get(segmentId) ?? NO_HIGHLIGHTS,
    speakers: listSpeakers(state.speakers),
  };
}

/**
 * Hydrates a stored frame list straight to a publishable snapshot.
 */
export function hydrateAnalysis(frames: readonly LiveFrame[]): LiveAnalysis {
  return analysedSnapshot(foldAnalysisFrames(frames));
}
