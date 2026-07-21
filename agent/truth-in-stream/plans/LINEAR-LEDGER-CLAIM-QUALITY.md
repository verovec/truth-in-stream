# Linear ledger - Epic E: claim detection quality

Linear was unreachable when this epic was authored (2026-07-21, LINEAR_API_KEY returns 401).
Replay once the connector re-authenticates: create the label `epic:claim-quality`, create the
three cards below directly in Todo state, priority High, team Veroveit, project Truth in
Stream, apply the label, then add blocks relations E1 -> E2 -> E3 and replace E1..E3 in the
plan files with the real VER numbers. States at delivery time are tracked in the roadmap.

---

## E1: No-source claims are unverifiable and every rationale is French

**Outcome**

A claim that retrieves no supporting sources is always classified unverifiable, and every claim explanation shown to viewers is written in French. Credible (green) and disputed (red) verdicts only ever appear when at least one validated retrieved source backs them.

**Context**

On the verify path (`FACTCHECK_VERIFY_PATH`), a claim that retrieved zero evidence currently still goes to the verifier when `FACTCHECK_KNOWLEDGE_FALLBACK` is on (the default), which can return a knowledge-basis credible or disputed verdict with capped confidence. The product rule changes: without sources the verdict is unverifiable, full stop. Rationale language is currently locale-dependent on the credibility path (English by default); the political path is already French-only. This card is the first of the claim-quality chain; the extraction card stacks on it because both edit `internal/service/verify_path.go`.

**Approach**

Backend only, respecting the `cmd/server` -> `handler` -> `service` -> `store` layering. Retire the knowledge fallback: remove `FACTCHECK_KNOWLEDGE_FALLBACK` from config and wiring (`internal/config/config.go`, `cmd/server/main.go`, `internal/service/verify_path.go`, `internal/service/political_path.go`, `internal/service/video_analyzer.go`) so all four no-evidence call sites (credibility and political, live and batch) short-circuit to the unverifiable outcome without a model call. Give that outcome a fixed French rationale, for example "Aucune source n'a pu etre trouvee pour verifier cette affirmation." After the verifier runs, demote any verdict whose basis is not evidence to unverifiable while keeping the model's rationale: the citation guard in `internal/verify/verify.go` (ValidateCitations) already demotes uncited evidence verdicts to knowledge basis, so with this rule a credible or disputed verdict always carries at least one validated citation. Apply the same demotion on the political literal axis. Make the rationale French in every locale by adding the explicit French-rationale instruction to the English credibility prompt (verdict enum tokens stay English). Update `docs/` where the fallback is documented. No frontend change; the existing verdict vocabulary is unchanged on the wire.

**Acceptance criteria**

* A claim with zero retrieved evidence is emitted verified/unverifiable with a French rationale and no verifier model call, on live and batch, credibility and political paths.
* A verdict the verifier grounds in no valid citation (knowledge basis) is emitted unverifiable, never credible or disputed; its French rationale is preserved.
* A verdict backed by at least one validated citation keeps its credible or disputed verdict, source label, and source URL exactly as today.
* Every rationale delivered on the wire (`claim_result` frames, document claims) is French regardless of locale configuration.
* `FACTCHECK_KNOWLEDGE_FALLBACK` no longer exists in config, wiring, or docs.

**Implementation todos**

- [ ] Remove KnowledgeFallback from `internal/config/config.go` (default, struct field, env read) and its tests
- [ ] Remove the fallback branches at the four no-evidence call sites; short-circuit to the unverifiable outcome with the fixed French rationale
- [ ] Drop KnowledgeFallback from `cmd/server/main.go` wiring and `internal/service/video_analyzer.go`; adjust the second-pass floor coupling accordingly
- [ ] Enforce basis=evidence for credible and disputed after the citation guard, on both the credibility and the political literal axes; keep the model rationale on demotion
- [ ] Add the French-rationale instruction to the English credibility prompt in `internal/verify/verify.go`
- [ ] Update affected tests (`internal/service/verify_path*_test.go`, `internal/service/political_path_test.go`, `internal/verify/*_test.go`, `internal/config/config_test.go`) and docs
- [ ] End-to-end check: run the stack, stream a claim with no matching corpus entry, observe an unverifiable verdict with a French rationale

**Definition of Done**

- [ ] Current best practice verified before any new dependency (none expected)
- [ ] `go test -race ./...` green; `go vet ./...`, `gofumpt`, `golangci-lint run ./...` clean
- [ ] Errors wrapped with `%w`; dead fallback code deleted, not commented out
- [ ] No secrets or infrastructure identifiers committed
- [ ] Branch rebased on the integration branch; PR CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify quality findings, re-review after changes; no merge until the review passes
---

## E2: Claim extraction reads the previous group and quotes only the claim core

**Outcome**

Claims are still detected on the current group of sentences, but the decomposer now sees the entire previous group as context, so references to what was just said resolve correctly. The quote each claim anchors to is the tightest span that carries the checkable assertion, not the whole sentence. The claims frame names the unit's member statements so the client can render the group as one statement.

**Context**

Live analysis groups up to four sentences per unit (`LIVE_MAX_SENTENCES`, `internal/service/live.go`); today only the previous unit's last sentence (25-word cap) is passed as decomposition context, and quotes often cover whole sentences. Stacks on the verdict-policy card (both edit `internal/service/verify_path.go`); the frontend merge card stacks on this one because it consumes the new wire field.

**Approach**

Backend only. Carry the full previous unit as context: replace the trailing-sentence `contextTail` in `internal/service/live.go` with the previous unit's complete speaker-labelled text, capped defensively (about 120 words, keeping the most recent sentences) so the prompt stays bounded; mirror on the batch path (`internal/service/document_analyzer.go`) by passing the previous sentence group instead of a single sentence. Keep the prompt rule that context is for reference resolution only and never a claim source, in both prompt languages (`internal/claimdecomp/claimdecomp.go`). Tighten quote minimality in both prompts: the quote is the shortest contiguous verbatim run carrying the claim's core (named subject, figure, predicate), typically well under a full sentence; a whole-sentence quote is acceptable only when every word is load-bearing. Add the unit's ordered member segment ids to the claims event (`internal/service/verify_path.go` scoreUnit and the `LiveEvent` shape in `internal/service/live.go`) and to the wire frame (`internal/handler/live.go` claimsFrame, new field `segment_ids`), strictly additive so an older client ignores it; the shared mapper feeds both the WebSocket and the REST hydration endpoint so analysed replay carries it too. Span anchoring (`internal/service/claimspan.go`) is mechanically unchanged; add fixture tests asserting sub-sentence quotes anchor correctly.

**Acceptance criteria**

* The decomposer request for a unit contains the full previous unit's text as context (speaker-labelled, bounded), on live and batch paths; the first unit of a session sends empty context.
* No claim is extracted from context-only content (prompt rule exercised by adapter-seam tests).
* Claims frames on the WebSocket and in REST replay carry `segment_ids` listing the unit's member subtitle ids in order; frames without the field still parse everywhere.
* Decomposer contract fixtures assert quotes are verbatim sub-spans of the statement and anchor to spans via the existing span computation.

**Implementation todos**

- [ ] Replace the single-sentence `contextTail` with full-previous-unit context under a word cap; update `internal/service/live.go` tests
- [ ] Pass the previous sentence group as context in `internal/service/document_analyzer.go`
- [ ] Strengthen quote-minimality wording in both system prompts in `internal/claimdecomp/claimdecomp.go`; keep claim-language rules unchanged
- [ ] Add member segment ids to the claims `LiveEvent` and `claimsFrame` (`segment_ids`), populated from the unit's members
- [ ] Extend handler tests to assert `segment_ids` on WebSocket and video-analysis replay outputs
- [ ] Table-driven tests for the new context builder: speaker changes, cap trimming, empty history
- [ ] End-to-end check: stream a two-unit exchange where the second unit needs the first for coreference; verify resolved claim text and tight highlight spans

**Definition of Done**

- [ ] `go test -race ./...` green; `go vet ./...`, `gofumpt`, `golangci-lint run ./...` clean
- [ ] Wire change additive; no frontend edits in this card
- [ ] Errors wrapped with `%w`; no dead code left behind
- [ ] No secrets or infrastructure identifiers committed; branch rebased; PR CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify quality findings, re-review after changes; no merge until the review passes
---

## E3: Live view merges a claim unit into one statement with claim-core highlights

**Outcome**

When a group of sentences produced claims, the live transcript shows it as one merged statement (speaker, combined time span, full text) instead of three or four separate rows, with the per-claim verdict rows beneath it. Inside the merged statement only the exact claim-core words carry a wash: neutral while pending or unverifiable, green when credible, red when disputed. Sentences without claims keep today's per-statement rows.

**Context**

`LiveStatementList` (`stack/frontend/src/app/app/_components/live-statement-list.tsx`) renders one row per subtitle statement. Claims frames now carry `segment_ids` naming the unit's members, delivered by the extraction card this one stacks on. The change applies to live, TV, and analysed playback alike because replay hydrates through the same reducers (`stack/frontend/src/lib/live/hydrate.ts`).

**Approach**

Frontend only, reducer-first, keeping Server Components by default and `'use client'` at existing leaves. Parse the optional `segment_ids` field in `stack/frontend/src/lib/live/frames.ts` (additive, validated). Record unit membership in the claims state (`stack/frontend/src/lib/live/claims.ts`) and derive merged display groups in the statements layer (`stack/frontend/src/lib/live/statements.ts` or a dedicated selector) so `LiveStatementList` renders one list item per claim-bearing unit: ordered member texts joined, speaker label, earliest start and latest end, positioned where its members were with newest-first ordering preserved. Keep per-segment span slicing (`stack/frontend/src/lib/live/highlight.ts`) intact inside the merged row so rune offsets stay valid per original segment. A claims frame with a single member, without `segment_ids`, or with unknown ids falls back to today's per-statement rendering. Adjust `HIGHLIGHT_VERDICT_CLASSES` so a verified-unverifiable claim uses the neutral wash instead of the grey tint; credible stays green, disputed stays red; badges and claim rows keep their existing styling. Vitest coverage for parser, reducers, selector, and component; run vitest from `stack/frontend`.

**Acceptance criteria**

* A unit of several sentences with at least one claim renders as one statement row in live view and analysed playback, positioned where its members were.
* Merged rows highlight only the claim-core spans; the surrounding sentence text is unstyled.
* The highlight wash is neutral for pending, checking, and unverifiable; green for credible; red for disputed.
* Statements with no claims, frames without `segment_ids`, and stale snapshots render exactly as before.
* The existing highlight guard (quote re-check against stale offsets) still holds on merged rows.

**Implementation todos**

- [ ] Parse optional `segment_ids` in `frames.ts` with validation tests
- [ ] Record unit membership in `claims.ts` state; expose a merged-statement selector
- [ ] Render merged rows in `live-statement-list.tsx`; keep `HighlightedStatementText` slicing per original segment
- [ ] Move verified-unverifiable to the neutral wash in `HIGHLIGHT_VERDICT_CLASSES`
- [ ] Update reducer and component tests (`claims.test.ts`, `highlight.test.ts`, `hydrate.test.tsx`, `live-statement-list.test.tsx`)
- [ ] End-to-end check: a live session where a three-to-four sentence unit yields claims renders one merged statement with tight coloured highlights; the same video replayed analysed matches

**Definition of Done**

- [ ] Vitest green from `stack/frontend`; ESLint clean; TypeScript compiles clean
- [ ] No backend edits in this card
- [ ] No secrets or infrastructure identifiers committed; branch rebased; PR CI green

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify quality findings, re-review after changes; no merge until the review passes
