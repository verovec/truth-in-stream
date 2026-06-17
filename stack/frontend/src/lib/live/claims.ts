// Incremental per-claim fact-check state for the retrieve-then-verify path. A
// checkable unit fans into atomic claims (a claims frame), and each claim has
// its own verdict that updates in place as claim_result frames arrive. This is a
// pure reducer, kept separate from the legacy statements store so a stream that
// emits no claim frames flows through statements unchanged - full backward
// compatibility - while a verify-path stream renders progressive per-claim
// disclosure. Claims are grouped under their unit id (shared with the unit's
// subtitle) and keyed on claim_id so a checking placeholder is replaced in place
// by its verdict.
import type { SegmentMatch } from "@/lib/fact-check/api";
import type {
  ClaimResultFrame,
  ClaimsFrame,
  ClaimStatus,
  ClaimVerdict,
  VerdictSource,
} from "./frames";

// LiveClaim is one atomic claim on screen with its current lifecycle state. The
// verdict fields are present only once verified; status carries the row through
// pending -> checking -> a terminal state (verified, unchecked, or error). The
// shape is intentionally flat (status is not a discriminant) because a verified
// row's verdict can be absent on a degenerate frame, so a renderer must guard on
// the field, not only the status.
export type LiveClaim = {
  claimId: string;
  text: string;
  status: ClaimStatus;
  source?: VerdictSource;
  verdict?: ClaimVerdict;
  confidence?: number;
  rationale?: string;
  matches?: SegmentMatch[];
  skipReason?: string;
  error?: string;
};

// ClaimsState keys a unit's claims by claim_id under the unit's correlation id.
// order preserves the announced claim order (a claim_result may arrive before
// its claims frame on a reconnect replay, so order is appended to lazily) so the
// list renders deterministically regardless of arrival order.
export type ClaimsState = {
  byUnit: ReadonlyMap<string, ReadonlyMap<string, LiveClaim>>;
  order: ReadonlyMap<string, readonly string[]>;
};

export function emptyClaims(): ClaimsState {
  return { byUnit: new Map(), order: new Map() };
}

// applyClaimsFrame announces a unit's atomic claims, each pending a verdict. A
// claim already present (a verdict raced ahead of the claims frame on a
// reconnect replay) keeps its further-along state rather than being reset to
// pending; only its text is backfilled. The announced order becomes the unit's
// canonical order.
export function applyClaimsFrame(
  state: ClaimsState,
  frame: ClaimsFrame,
): ClaimsState {
  const byUnit = new Map(state.byUnit);
  const order = new Map(state.order);
  const claims = new Map<string, LiveClaim>(byUnit.get(frame.id));
  const claimOrder: string[] = [];
  for (const claim of frame.claims) {
    claimOrder.push(claim.claimId);
    const existing = claims.get(claim.claimId);
    if (existing && existing.status !== "pending") {
      // A result landed before this announcement: keep the verdict, backfill the
      // text the announcement carries.
      claims.set(claim.claimId, { ...existing, text: claim.text });
    } else {
      claims.set(claim.claimId, {
        claimId: claim.claimId,
        text: claim.text,
        status: "pending",
      });
    }
  }
  byUnit.set(frame.id, claims);
  order.set(frame.id, claimOrder);
  return { byUnit, order };
}

// applyClaimResultFrame replaces one claim's row in place, keyed on claim_id. A
// terminal state (verified, unchecked, error) never downgrades back to checking
// if a stale frame arrives out of order. A result for a claim not yet announced
// (out-of-order delivery or a reconnect replay) is recorded and appended to the
// order so it still renders.
export function applyClaimResultFrame(
  state: ClaimsState,
  frame: ClaimResultFrame,
): ClaimsState {
  const byUnit = new Map(state.byUnit);
  const order = new Map(state.order);
  const claims = new Map<string, LiveClaim>(byUnit.get(frame.id));

  const existing = claims.get(frame.claimId);
  if (
    existing &&
    isTerminalClaimStatus(existing.status) &&
    frame.status === "checking"
  ) {
    // A late checking placeholder must not erase a verdict already shown.
    return state;
  }

  const next: LiveClaim = {
    claimId: frame.claimId,
    text: existing?.text ?? "",
    status: frame.status,
    source: frame.source,
    verdict: frame.verdict,
    confidence: frame.confidence,
    rationale: frame.rationale,
    matches: frame.matches,
    skipReason: frame.skipReason,
    error: frame.error,
  };
  claims.set(frame.claimId, next);
  byUnit.set(frame.id, claims);

  if (!hasOrder(order, frame.id, frame.claimId)) {
    const prior = order.get(frame.id) ?? [];
    order.set(frame.id, [...prior, frame.claimId]);
  }
  return { byUnit, order };
}

// isTerminalClaimStatus is the single definition of claim terminality the
// reducer and the running summary both key off, so the strip and the per-claim
// list can never disagree on whether a unit is still in progress. A verdict, a
// capacity shed, and a failure are all terminal; pending and checking are not.
export function isTerminalClaimStatus(status: ClaimStatus): boolean {
  return (
    status === "verified" || status === "unchecked" || status === "error"
  );
}

function hasOrder(
  order: ReadonlyMap<string, readonly string[]>,
  unitId: string,
  claimId: string,
): boolean {
  return (order.get(unitId) ?? []).includes(claimId);
}

// claimsForUnit returns a unit's claims in announced order for rendering, or an
// empty array when the unit decomposed into no claims (legacy stream, or a unit
// gated as not-a-claim). A claim id in the order with no entry (defensive) is
// skipped.
export function claimsForUnit(
  state: ClaimsState,
  unitId: string,
): LiveClaim[] {
  const claims = state.byUnit.get(unitId);
  if (!claims) {
    return [];
  }
  const ordered: LiveClaim[] = [];
  for (const claimId of state.order.get(unitId) ?? []) {
    const claim = claims.get(claimId);
    if (claim) {
      ordered.push(claim);
    }
  }
  return ordered;
}

// dropUnits removes the claims of the given unit ids, used when a session is torn
// down (seek or a dropped connection) so in-flight claims of statements that were
// themselves dropped do not linger. Units still present keep their claims.
export function dropUnits(
  state: ClaimsState,
  keep: ReadonlySet<string>,
): ClaimsState {
  const byUnit = new Map<string, ReadonlyMap<string, LiveClaim>>();
  const order = new Map<string, readonly string[]>();
  for (const [unitId, claims] of state.byUnit) {
    if (keep.has(unitId)) {
      byUnit.set(unitId, claims);
      const o = state.order.get(unitId);
      if (o) {
        order.set(unitId, o);
      }
    }
  }
  return { byUnit, order };
}
