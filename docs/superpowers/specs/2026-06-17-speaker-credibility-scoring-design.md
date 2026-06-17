# Speaker-credibility fact-checking — design

Date: 2026-06-17
Status: draft (brainstorming), pending user spec review
Supersedes the verdict-vocabulary portion of
[2026-06-17-retrieve-then-verify-factcheck-design.md](2026-06-17-retrieve-then-verify-factcheck-design.md);
the retrieve-then-verify architecture (gate -> decompose -> fast/verify pools, citation guard,
golden eval, `FACTCHECK_VERIFY_PATH` flag) is unchanged except where stated here.

## Problem

The verify path's verifier is a strict, corpus-only entailment judge. Its prompt bars outside
knowledge and returns `not_enough_info` whenever the retrieved passages do not literally settle the
claim. In practice this produces verdicts like:

> "Most people slip into addiction gradually." -> **Not enough info, 95%** —
> *"The evidence passages do not contain information about whether most people slip into addiction
> gradually."*

This is wrong as a *product*. We are not auditing whether our corpus happens to contain a sentence;
we are answering a viewer's question: **"Can I trust this speaker on what he just said?"** Requiring
the speaker to state only things our corpus pre-contains makes the tool silent on most of what is
actually said, and frames an unremarkable, broadly-true statement as a failure.

## Goal

Reframe the verify path from **strict entailment** to **speaker credibility**:

1. The default posture flips. A claim is trusted unless we find a reason to doubt it. We flag
   contradiction and implausibility; we stay quiet (credible) otherwise.
2. Per-claim verdict vocabulary becomes **`credible` / `disputed` / `unverifiable`**, each with a
   confidence and a grounding basis (evidence-backed vs knowledge-only).
3. A new **running per-speaker credibility score** aggregates a speaker's checked claims with
   Bayesian shrinkage toward neutral, so a speaker with one true claim does not read as a confident
   100%, and the score sharpens as they say more.

Non-goals (unchanged from the parent spec): live web search (corpus-only retrieval); transcription,
diarization, per-speaker buffering, the gate, decomposition, the two-pool concurrency model. This
change rides entirely on the existing `FACTCHECK_VERIFY_PATH` flag (default off).

## Decisions (from brainstorming)

- **Verdict basis:** a credibility score, not strict true/false. Flag only contradiction or
  implausibility; otherwise trust.
- **Knowledge source:** evidence-anchored, model world-knowledge as a *tiebreaker*. Default to
  retrieved evidence; fall back to world knowledge only when no relevant evidence exists, and mark
  those verdicts clearly as lower-confidence / "no sources" so the UI distinguishes them.
- **Verdict states:** three — `credible`, `disputed`, `unverifiable` — plus a per-claim score and a
  global per-speaker average.
- **Speaker aggregate:** Bayesian shrinkage to a neutral prior; only `credible`/`disputed` move the
  score; `unverifiable` is excluded from the score but shown as a separate tally.

## Architecture

Everything upstream of the verdict is unchanged. Two layers change, plus one new layer:

```
per claim (verify path) -> verifier emits credibility verdict {state, confidence, basis, citations, rationale}
                              |
                              v
                        citation guard (reworked: validates evidence-basis verdicts; demotes basis, not state)
                              |
                              v
                        per-speaker aggregator (NEW): Beta-Binomial shrinkage -> running speaker score
                              |
                              v
                        events: per-claim LiveEventResult (new labels) + LiveEventSpeakerScore (NEW)
                              |
                              v
                        frontend: verdict badges (relabeled) + speaker-credibility widget (NEW)
```

## Components

### Verifier vocabulary + prompt (`internal/verify`, reworked)

The tool enum changes from `supports | refutes | not_enough_info` to
`credible | disputed | unverifiable`, and the tool gains a `basis` field:

```
record_verdict({
  state:      "credible" | "disputed" | "unverifiable",
  basis:      "evidence" | "knowledge",      // what the judgment rests on
  confidence: 0.0-1.0,
  citations:  [{evidence_id, quoted_span}],  // required only when basis == "evidence"
  rationale:  string
})
```

Prompt rewrite (evidence-anchored, knowledge-as-tiebreaker):

- **First, judge against the supplied evidence.** If a passage directly affirms the claim ->
  `credible`, `basis: evidence`. If a passage directly contradicts it -> `disputed`,
  `basis: evidence`. These MUST cite the passage.
- **If no supplied passage bears on the claim, fall back to general knowledge** as a tiebreaker:
  - broadly true / consistent with well-established consensus -> `credible`, `basis: knowledge`;
  - clearly false / contradicts well-established consensus -> `disputed`, `basis: knowledge`;
  - genuinely indeterminate, or a private/anecdotal/subjective claim no general knowledge can speak
    to -> `unverifiable` (no citations).
- A `basis: knowledge` verdict must keep confidence modest (the prompt instructs, and the
  deterministic layer caps it — see below). This is the controlled re-introduction of world
  knowledge the parent spec deliberately barred: it is allowed, but is always lower-confidence and
  visibly sourced as "no direct sources."

`defaultModel` stays Claude Haiku; escalation hook unchanged.

### Citation guard (`ValidateCitations`, reworked)

Today the guard downgrades an unsupported `supports`/`refutes` to `not_enough_info`. The new
semantics demote the **basis, not the state**:

- `basis: evidence` with no surviving valid citation -> re-mark `basis: knowledge` and apply the
  knowledge confidence cap. The claim is not forced to `unverifiable`: the model may still hold a
  defensible knowledge-based judgment; it just loses its evidence grounding.
- `basis: knowledge` -> citations are not required; any stray citations are still validated and
  dropped if fabricated.
- `unverifiable` -> citations cleared; confidence floored low; never upgraded.
- Knowledge confidence cap: a deterministic constant (e.g. `<= 0.6`) applied to every
  `basis: knowledge` verdict so a knowledge-only call can never render as high-confidence. Evidence
  verdicts keep the model's clamped `[0,1]` confidence.

This keeps the anti-hallucination property (a fabricated citation can never prop up a verdict) while
allowing the credibility framing.

### Per-speaker credibility aggregator (`internal/service`, new)

A small, pure, unit-testable scorer held per speaker (alongside the existing `speakerMemory`).
Beta-Binomial shrinkage toward a neutral prior:

- Prior `Beta(k/2, k/2)` — symmetric, mean 0.5. `k` is the prior strength (pseudo-count),
  config-tunable (`SPEAKER_SCORE_PRIOR_STRENGTH`, default 4). Larger `k` = slower to move.
- Each `credible` claim adds its `confidence` to successes `S`; each `disputed` adds its
  `confidence` to failures `F`. `unverifiable` adds to neither.
- Score = posterior mean = `(k/2 + S) / (k + S + F)`, surfaced as a percentage.
  - 0 claims -> 50%. One full-confidence credible claim (k=4) -> `(2+1)/(4+1)` = 60%, not 100% —
    matching the requirement that a single true claim not read as a confident max.
  - Converges to the confidence-weighted true rate as claims accumulate.
- Tallies carried alongside the score: `credible`, `disputed`, `unverifiable` counts, so the UI can
  show "62% · 5 checked · 2 unverifiable" and de-emphasize a thin sample.

Confidence-weighting each observation means a tentative knowledge-only verdict moves the score less
than a strongly-evidenced one — desirable. The aggregator is fed from the per-claim verdict at the
point a verdict is emitted (curated fast-path and verified path alike; `unverifiable` from either
still only updates the tally).

### Event model (additive)

`LiveEventResult` is unchanged in shape; the verdict it carries uses the new `state`/`basis` fields.
One new event:

```
LiveEventSpeakerScore  { speaker, score (0..1), credible, disputed, unverifiable }
```

emitted after each claim verdict updates a speaker's aggregate. Client renders/updates a per-speaker
credibility widget keyed on speaker id. Additive: a client that ignores it still works.

Curated fast-path mapping: a borrowed curated `supports` -> `credible` `basis: evidence`; curated
`refutes` -> `disputed` `basis: evidence` (the curated claim is the cited source). No separate
redesign of the fast path.

### Frontend (relabel + new widget)

- `verdict-badge.tsx`: render `credible` (positive), `disputed` (negative), `unverifiable`
  (neutral/muted); a `basis: knowledge` verdict shows a "no direct sources" affordance and the muted
  confidence. The example claim now reads e.g. *"Credible · based on general knowledge"* instead of
  *"Not enough info, 95%."*
- New per-speaker credibility widget: the running score with its sample-size tally, visually
  de-emphasized while the sample is thin.
- `lib/live/frames.ts`, `lib/live/claims.ts`, `lib/live/summary.ts`: parse the new labels + the new
  speaker-score frame; the live findings summary counts credible/disputed/unverifiable.

### Eval (`internal/eval`, relabeled)

`golden.json` is relabeled from `supports/refutes/not_enough_info` to `credible/disputed/
unverifiable`. The 20 adversarial same-topic-opposite-truth cases map: contradicted -> `disputed`;
the same-topic-irrelevant and broadly-true-but-uncorpused cases are re-labelled to their credibility
expectation (`credible` `basis: knowledge`, or `unverifiable`) with refreshed provenance notes. The
harness and `baselineAccuracy` gate are retained; the offline faked-model run still guards wiring +
the citation/basis logic. The real-model eval + flag-flip procedure from the parent spec still
governs flipping `FACTCHECK_VERIFY_PATH` on.

## Testing

No behaviour ships without tests (`-race`, table-driven, fake LLM clients).

- **Verifier:** the addiction example -> `credible` `basis: knowledge`; an evidence-contradicted
  claim -> `disputed` `basis: evidence` with a citation; a same-topic-irrelevant passage no longer
  forces a verdict — knowledge tiebreaker decides; a private/anecdotal claim -> `unverifiable`.
- **Citation guard:** evidence-basis verdict with fabricated citation -> demoted to
  `basis: knowledge` + capped confidence (not forced unverifiable); knowledge verdict needs no
  citation; confidence cap applied.
- **Aggregator (pure):** 0 claims -> 0.5; one credible -> shrunk (not 1.0); converges with volume;
  `unverifiable` moves only the tally; confidence-weighting; prior-strength config respected.
- **Live orchestration:** a verified claim emits `LiveEventSpeakerScore` with correct running
  counts; curated mapping (`supports`->credible, `refutes`->disputed).
- **Frontend:** badge rendering for all three states + the knowledge "no sources" affordance; the
  speaker widget updates across frames; summary counts.

## Rollout

Rides the existing `FACTCHECK_VERIFY_PATH` flag (default off); `main` stays shippable. Delivered as
one card on one branch (per request), implemented in this order so each step is independently green:

1. `internal/verify`: vocabulary + `basis` + prompt rewrite + reworked citation guard + tests.
2. `internal/service`: per-speaker aggregator (pure) + config prior strength + tests.
3. Wire aggregator into the verify path's verdict-emit points; add `LiveEventSpeakerScore`; map the
   curated fast-path verdicts; orchestration tests.
4. Frontend: relabel badges, parse new frames, add the speaker-credibility widget; Vitest.
5. Relabel `golden.json` + refresh provenance; keep the accuracy gate green.

## Open question for review

Prior strength `k` default (proposed 4) and the knowledge confidence cap (proposed 0.6) are the two
tunable constants that shape how the score and knowledge verdicts feel. They are config/constants so
they can be tuned after seeing live behaviour, but the defaults are a judgment call worth confirming.
