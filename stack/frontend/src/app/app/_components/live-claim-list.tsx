"use client";

import { useState } from "react";
import type { ClaimVerdict, VerdictSource } from "@/lib/live/frames";
import type { LiveClaim } from "@/lib/live/claims";
import { MatchRow } from "./match-row";

// LiveClaimList renders one statement's atomic claims on the retrieve-then-verify
// path: each claim is a row keyed on claim_id that progresses pending -> checking
// -> a verdict (or an honest terminal state), updating in place. A verified row
// reveals its citations and rationale on tap. It renders nothing when the unit
// decomposed into no claims (legacy stream or a not-a-claim unit), so a stream
// that emits no claim frames is unaffected.
export function LiveClaimList({ claims }: { claims: LiveClaim[] }) {
  if (claims.length === 0) {
    return null;
  }
  return (
    <ul aria-label="Atomic claims" className="flex flex-col gap-1.5 pb-1">
      {claims.map((claim) => (
        <ClaimRow key={claim.claimId} claim={claim} />
      ))}
    </ul>
  );
}

// VERDICT_LABELS renders the verify path's credibility verdict enum in
// reader-facing terms. unverifiable is a first-class verdict, shown as
// "Unverifiable" rather than an error or an empty row.
const VERDICT_LABELS: Record<ClaimVerdict, string> = {
  credible: "Credible",
  disputed: "Disputed",
  unverifiable: "Unverifiable",
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
  curated: "from a curated source",
  verified: "checked against evidence",
};

const SOURCE_STYLES: Record<VerdictSource, string> = {
  curated:
    "bg-violet-100 text-violet-700 dark:bg-violet-500/15 dark:text-violet-300",
  verified:
    "bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300",
};

function ClaimRow({ claim }: { claim: LiveClaim }) {
  return (
    <li className="rounded-md border border-zinc-200 px-2.5 py-1.5 dark:border-zinc-800">
      <p className="text-xs leading-5 text-zinc-700 dark:text-zinc-300">
        {claim.text}
      </p>
      <ClaimState claim={claim} />
    </li>
  );
}

// ClaimState renders the lifecycle marker for one claim by its status. A
// terminal verdict is expandable for citations and rationale; checking shows a
// spinner; unchecked and error are honest terminal states, never a crash or a
// blank row.
function ClaimState({ claim }: { claim: LiveClaim }) {
  if (claim.status === "pending" || claim.status === "checking") {
    return (
      <p
        role="status"
        className="mt-1 flex items-center gap-1.5 text-[11px] text-zinc-500 dark:text-zinc-400"
      >
        <span
          aria-hidden="true"
          className="size-1.5 animate-pulse rounded-full bg-sky-500"
        />
        {claim.status === "pending" ? "Queued for checking…" : "Checking…"}
      </p>
    );
  }

  if (claim.status === "unchecked") {
    return (
      <p className="mt-1 text-[11px] italic text-zinc-400 dark:text-zinc-500">
        Not checked - the verifier was at capacity.
      </p>
    );
  }

  if (claim.status === "error") {
    return (
      <p className="mt-1 text-[11px] text-amber-700 dark:text-amber-400">
        This claim could not be checked.
      </p>
    );
  }

  // Verified: a verdict landed. A degenerate verified frame with no verdict
  // (defensive) reads as not-enough-info rather than a blank row.
  return <VerifiedClaim claim={claim} />;
}

function VerifiedClaim({ claim }: { claim: LiveClaim }) {
  const [expanded, setExpanded] = useState(false);
  const verdict = claim.verdict ?? "unverifiable";
  const matches = claim.matches ?? [];
  const hasDetail = matches.length > 0 || Boolean(claim.rationale);

  return (
    <div className="mt-1 flex flex-col gap-1">
      <div className="flex items-center gap-1.5">
        <span
          className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${VERDICT_STYLES[verdict]}`}
        >
          {VERDICT_LABELS[verdict]}
        </span>
        {claim.basis === "knowledge" ? (
          // A knowledge-basis verdict rests on the model's general knowledge, not a
          // retrieved passage, so it is marked as having no direct sources and the
          // viewer can weigh it as lower-confidence.
          <span className="inline-flex items-center rounded-full bg-amber-100 px-1.5 py-0.5 text-[10px] font-medium text-amber-700 dark:bg-amber-500/15 dark:text-amber-300">
            no direct sources
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
      {hasDetail ? (
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => setExpanded((prev) => !prev)}
          className="self-start text-[11px] font-medium text-sky-700 underline decoration-dotted underline-offset-2 hover:decoration-solid focus-visible:outline-2 focus-visible:outline-sky-500 dark:text-sky-400"
        >
          {expanded ? "Hide reasoning" : "Show reasoning"}
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
