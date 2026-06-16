# Retrieve-then-verify fact-checking — design

Date: 2026-06-17
Status: approved (brainstorming), pending implementation plan

## Problem

The live fact-checker produces verdicts that are shallow or wrong, and skips too many
statements. Both symptoms share one root cause: the system is **closed-corpus**. A verdict is
decided by embedding similarity against a curated `claims` table plus Wikipedia chunks:

- When a near-paraphrase of a curated claim exists, the verdict is **borrowed** from that table.
- When no near-match exists, the statement is gated out as `not_covered` and never checked.

Two consequences:

1. **Similarity is not entailment.** Cosine similarity measures topical relatedness, not whether
   the evidence supports or refutes the statement. "The vaccine causes autism" and "studies show
   the vaccine does not cause autism" embed almost identically. The current confidence formula
   `Supporting / (Supporting + Contradicting)` weights matches by similarity, so a same-topic
   passage counts as support regardless of what it actually says. Nothing on the live path ever
   *reads* the evidence against the statement.
2. **Coverage is a symptom of having no reasoning layer.** Because the system can only adjudicate
   what it can pre-match, "is there a near neighbor?" doubles as the gate. Genuinely novel but
   perfectly checkable claims are dropped.

## Goal

Add a **retrieve-then-verify reasoning layer** and demote the coverage gate from "do I already
have the answer?" to "is this worth checking?". Verdicts become *derived* from evidence the model
actually reads, per atomic claim, with `not_enough_info` as a first-class verdict instead of a
pre-emptive skip.

Non-goals for v1: live web search (corpus-only verification); replacing the transcription,
diarization, or per-speaker buffering stages (unchanged).

## Architecture

Everything upstream of the verdict is unchanged: transcribe (AssemblyAI) -> diarized segments ->
buffer into per-speaker units. The redesign changes only what happens after a unit exists, and
splits it into two paths behind one event stream (tiered hybrid).

```
per-speaker unit
   |
   v
GATE: "is this a factual, check-worthy claim?"        (heuristic + Haiku check-worthy; NO corpus lookup)
   | checkable
   v
DECOMPOSE -> 1..N atomic claims                        (coreference-resolved, self-contained)
   |
   |--- FAST PATH (per claim) -------------------------|
   |  embed -> ANN over curated claims                 |
   |  high-confidence near-match (>= tau_fast)?         |
   |     yes -> emit verdict now (borrowed, instant)   |
   |---------------------------------------------------|
   | no fast verdict
   v
VERIFY PATH (per claim)
   retrieve evidence (claims + wiki, broadened recall)
   LLM judge: supports / refutes / not_enough_info + cited spans + rationale
   emit verdict (streams in ~1-3s)
```

Three spine-level changes:

1. **The gate stops doing coverage.** `not_covered` (the embedding-coverage check in
   `service/coverage.go`) is removed from the gate. The gate answers only "is this a factual claim
   worth checking?" using the existing heuristic + Haiku check-worthy step. Whether evidence
   exists is discovered by the verify path, not pre-judged. This reclaims skipped statements.
2. **Verdicts are derived, per atomic claim.** A unit fans out into atomic claims; each gets its
   own verdict. The fast path may still borrow a curated verdict for a near-paraphrase (instant,
   no LLM); everything else is judged by a model that reads evidence against the claim and returns
   supports / refutes / not_enough_info.
3. **Two worker pools, one event stream.** Fast path emits a verdict immediately; verify path
   emits a `checking` placeholder then a follow-up verdict for the same `claim_id`. Reuses the
   existing worker-pool + bounded-queue model from `service/live.go`.

Defaults: verify path is **corpus-only** in v1 (no live web search); verifier model is **Claude
Haiku** (matches existing check-worthy/stance usage), with an optional escalation-to-Sonnet hook
specced but off by default.

## Components

### Claim decomposition (`internal/claimdecomp`, new)

One LLM call per checkable unit (Haiku, forced tool, temp 0). Input: unit text + speaker +
recent speaker context. Output: list of atomic claims, each a self-contained declarative sentence
with coreference resolved.

- Resolve pronouns/context so each claim survives out of the transcript.
- Drop non-factual fragments (opinions, hedges) — decomposition doubles as a finer check-worthy
  filter.
- Cap at `MaxClaimsPerUnit` (default 4); past the cap, keep the most check-worthy.
- Empty list -> unit emits a single skip event (no fan-out).
- Degradation: on model error, fall back to "treat the whole unit as one claim" so the live path
  never stalls.

### Verifier (`internal/verify`, new — the core of the fix)

One LLM call per claim that reaches the verify path. Forced tool, temp 0. Input: claim + top-K
retrieved evidence passages, each tagged with an `evidence_id`. Contract:

```
record_verdict({
  verdict:    "supports" | "refutes" | "not_enough_info",
  confidence: 0.0-1.0,                        // model's calibrated confidence
  citations:  [{evidence_id, quoted_span}],   // MUST cite retrieved evidence used
  rationale:  string                          // one sentence, shown on tap
})
```

Prompt instruction: judge only from supplied evidence; if it does not settle the claim, return
`not_enough_info`; every supports/refutes must cite at least one passage by id. A same-topic but
non-bearing passage yields NEI, not a false support — this is what kills the similarity-not-
entailment bug.

**Citation enforcement (deterministic post-validation):** after the call, verify each cited
`evidence_id` was actually sent and that `quoted_span` is a substring of that passage. Drop
fabricated citations; if a supports/refutes verdict has zero valid citations left, downgrade to
`not_enough_info`. Fully unit-testable; stops hallucinated grounding from reaching the UI.

### Evidence retrieval (`internal/service/match.go`, reworked)

The verify path needs higher recall, lower precision — the model is now the precision filter.

- Pull top-K (default 8) from claims and top-K from wiki at a looser floor than today. Prefer
  handing the verifier an irrelevant passage (ignored) over starving it into a false NEI.
- Keep title-prefixed wiki chunks for attribution.
- Each passage carries a stable `evidence_id` (kind + source id + chunk index) so citations
  round-trip.
- **Query is the atomic claim text, not the raw unit** — decomposition gives clean,
  coreference-resolved queries, improving recall over a messy multi-claim utterance.

### Confidence (reworked)

Delete `Supporting / (Supporting + Contradicting)`. Verdict and confidence come from the verifier,
grounded in evidence it read. Surfaced score = model `confidence` with deterministic adjustments:

- Multiply down when fewer than N independent sources were cited.
- Floor/cap so `not_enough_info` never renders as high-confidence.

Fast-path (borrowed) verdicts keep a similarity-derived confidence but are tagged
`source: "curated"` vs `source: "verified"` so the UI and offline eval can distinguish a borrowed
verdict from a reasoned one.

### Consistency (self-contradiction) — upgraded in place

Now compares atomic claims against a speaker's prior atomic claims (not whole units), reusing the
per-claim embeddings computed for retrieval. No new model surface.

## Event model & concurrency

Per-claim progressive disclosure. A unit fans into claims; each claim has a lifecycle the client
renders in place, keyed on `claim_id`:

```
LiveEventSubtitle                       unit text appears instantly (unchanged)
LiveEventClaims                         atomic claims, each claim_id + status:"pending"
  per claim:
    LiveEventResult status:"verified" source:"curated"    (fast path, sub-second)
      -- or --
    LiveEventResult status:"checking"                      (verify path placeholder)
    LiveEventResult status:"verified" source:"verified"    (verify path, ~1-3s, same claim_id)
```

Client replaces in place, so a claim visibly goes `checking... -> refuted [3 sources]`.

Concurrency (`service/live.go`): two pools instead of one.

- **Fast pool** — short deadline (~800ms), embedding + ANN only. Saturation -> claim queued to
  verify pool rather than shed.
- **Verify pool** — smaller, longer deadline (~4s), bounded queue. Saturation -> claim emits
  `status:"unchecked", reason:"capacity"` (existing shed semantics, now an honest per-claim
  terminal state, not a silent drop).
- Decomposition runs on the fast pool (one cheap call gating fan-out).
- Per-claim verify calls within a unit run concurrently, capped by a semaphore sized to the verify
  pool, so one multi-claim unit cannot starve the next speaker.

Cost/latency: the fast path absorbs the common curated-near-match case with zero LLM calls; only
novel claims pay for decomposition + verification. A short-TTL cache keyed on normalized claim
text collapses repeated claims (recurring debate talking points).

## Testing

No behaviour ships without tests (`-race` for Go; table-driven; fake LLM clients as in existing
`checkworthy`/`stance` tests).

- **Decomposition:** multi-claim splitting, coreference resolution, opinion-dropping, the
  `MaxClaimsPerUnit` cap, empty-list -> skip, error -> single-claim fallback.
- **Verifier:** adversarial entailment pairs that expose today's bug ("vaccine causes autism" vs
  a passage saying it does not -> `refutes`; same-topic-but-irrelevant passage -> NEI). Citation
  guard: fabricated `evidence_id` dropped; supports/refutes with zero valid citations downgraded
  to NEI.
- **Retrieval:** existing integration tests retuned for top-K + looser floor; assert
  `evidence_id` round-trips.
- **Live orchestration:** faked transcribe + pools assert the event lifecycle
  (`Subtitle -> Claims(pending) -> Result(checking) -> Result(verified)`), same `claim_id`,
  capacity-shed -> `unchecked`.
- **Golden eval set (new):** ~30-50 labelled statements with expected verdicts, run as a
  regression test to measure accuracy before/after and catch drift.

## Rollout (incremental, flagged)

A large change to the core path. Ships as parallel, independently testable cards behind
`FACTCHECK_VERIFY_PATH` (default off); the old path stays default until the golden eval shows
accuracy at least at the current baseline. `main` stays shippable throughout.

1. `claimdecomp` package + tests (no wiring).
2. `verify` package + tests + citation guard (no wiring).
3. `match.go` recall rework + `evidence_id` round-trip + tests.
4. Wire the verify pool + new events behind `FACTCHECK_VERIFY_PATH` (default off).
5. Frontend: per-claim progressive-disclosure rendering.
6. Golden eval set + accuracy gate; flip the flag on once baseline is met.
