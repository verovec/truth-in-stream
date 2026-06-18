"use client";

import type { LiveClaim } from "@/lib/live/claims";
import { VerifiedClaim } from "./live-claim-verdict";

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
    <ul aria-label="Affirmations atomiques" className="flex flex-col gap-1.5 pb-1">
      {claims.map((claim) => (
        <ClaimRow key={claim.claimId} claim={claim} />
      ))}
    </ul>
  );
}

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
        {claim.status === "pending" ? "En attente de vérification…" : "Vérification…"}
      </p>
    );
  }

  if (claim.status === "unchecked") {
    return (
      <p className="mt-1 text-[11px] italic text-zinc-400 dark:text-zinc-500">
        Non vérifiée — le vérificateur était à pleine capacité.
      </p>
    );
  }

  if (claim.status === "error") {
    return (
      <p className="mt-1 text-[11px] text-amber-700 dark:text-amber-400">
        Cette affirmation n&apos;a pas pu être vérifiée.
      </p>
    );
  }

  // Verified: a verdict landed. A degenerate verified frame with no verdict
  // (defensive) reads as not-enough-info rather than a blank row.
  return <VerifiedClaim claim={claim} />;
}
