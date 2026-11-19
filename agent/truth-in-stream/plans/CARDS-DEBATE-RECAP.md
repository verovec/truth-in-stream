# Cards - Epic G: debate recap

Authored 2026-11-19. These cards are tracked locally in this file by decision of the
operator, not in Linear. Ids are G1..G3, state `Todo`, priority High, label
`epic:debate-recap`. The chain is serial: `G1 -> G2 -> G3`.

The epic turns a finished video analysis into a readable report. Its reference is a report
produced by hand on a real 2h51 five-candidate debate: a cover, a recap of every speaker, then
one sheet per politician with sourced statements. That exercise set the layout and exposed the
two gaps these cards close: diarized voices carry letters instead of names, and no screen
presents an analysis as a report.

---

## G1: Post-analysis step names each diarized speaker

**Outcome**

When a video analysis completes, the operator sees real names on the speakers instead of the
diarization letters. Each name comes with a role, a confidence level and the transcript evidence
it was inferred from, and the operator can correct or confirm any of them.

**Context**

The live pipeline labels speakers `A`, `B`, `C`... as the streaming transcriber emits them, and
every stored event, per-claim telemetry row and speaker tally carries that letter. Nothing maps
a letter to a person. On the reference debate the mapping was recovered by hand from the stored
transcript alone, with four independent signals that agreed:

- Handoffs. A moderator turn ends on a name and the next voice answers (3 to 8 occurrences per
  candidate).
- Vocative adjacency. A voice speaks right before or after another speaker addresses someone by
  name (up to 14 occurrences for one candidate).
- Self-reference. A first-person biographical statement only one participant can make.
- Structure. Speaking time and turn length separate principals from moderators: five voices near
  28 to 34 minutes with long turns, two near 10 minutes with short turns, one marginal guest.

Counting how often a voice utters each name was the weakest signal and was misleading on its
own: diarization bleeds a moderator's sentence into a candidate's turn when they overlap.

This card feeds G2, which renders names in the recap. The stored analysis snapshot is immutable
and versioned; names must never rewrite it.

**Approach**

- Deterministic signals first, a generative call last. Score each (label, candidate name) pair
  from handoffs, vocative adjacency and self-reference over the stored final transcript turns.
  Use the shared forced-tool LLM client only to extract the list of participant names and to
  break ties the signals leave open; never add a provider client.
- Ground every name in the transcript or the video title. A name the model proposes without a
  quoted span that is an exact substring of a stored turn is discarded. Nothing comes from model
  memory. Store a name as it was heard: a guest introduced by first name only keeps the first
  name.
- A label below the confidence floor stays unnamed. A wrong name on a statement is worse than a
  letter.
- Persist the mapping in its own additive table keyed by (video, label): display name, role
  (`principal`, `moderator`, `guest`, `unknown`), confidence, method, evidence (quoted spans with
  timestamps), and whether an operator confirmed or overrode it. An operator-set name is never
  overwritten by a re-run.
- Run the step after the analysis is persisted, off the analysis path. A failure logs and leaves
  the analysis complete and unnamed; it must not fail or delay the analysis.
- Apply names at read time: the analysis read model and the SRT and CSV exports gain the
  resolved name next to the label. Respect the layering: HTTP only in handlers, no HTTP types in
  services, all SQL through generated queries.
- Verify current versions and best practice before adding any dependency.

**Acceptance criteria**

* After a video analysis completes, each diarized label has a stored proposal: name or
  unnamed, role, confidence and evidence.
* On a fixture reproducing the reference debate structure (five principals, two moderators, one
  guest, overlapping turns), every principal and moderator is named correctly and no label
  receives a wrong name.
* A label with contradictory or insufficient evidence is left unnamed rather than guessed.
* An operator can rename a speaker, set its role and confirm it; the change survives a re-run
  of the step.
* The step can be re-run on demand for an already analysed video.
* The analysis view, the speaker credibility panel, and the SRT and CSV exports show the
  resolved name, and fall back to the letter when there is none.
* A failure of the naming step leaves the analysis readable and marks the speakers unnamed.

**Implementation todos**

- [ ] Additive migration for the speaker mapping table, with its down migration, and the
  generated queries.
- [ ] Turn reconstruction from stored final transcript events: ordered turns per label with
  start, end and text.
- [ ] Participant name extraction from the video title and the transcript, grounded by exact
  quoted spans.
- [ ] Deterministic scorers: handoff, vocative adjacency, self-reference; role inference from
  speaking time and turn length.
- [ ] Score fusion with a confidence floor and a contradiction rule; tie-break call through the
  shared LLM client, validated against the transcript.
- [ ] Post-completion trigger in the video analysis job, isolated from the analysis outcome.
- [ ] Endpoints: read the mapping, re-run the step (admin), update one speaker (admin).
- [ ] Read model and exports carry the resolved name.
- [ ] Frontend: names in the analysis view and the speaker credibility panel; an admin control
  to rename, set role and confirm.
- [ ] Table-driven tests for each scorer, the fusion rules and the override-survives-re-run
  rule; a synthetic debate fixture (no real transcript committed).
- [ ] Documentation of the step, its signals and its limits.

**Definition of Done**

- [ ] Versions and best practice verified before any new dependency.
- [ ] Go tests green under `-race`; frontend tests green.
- [ ] `gofumpt`, `go vet`, `golangci-lint` and ESLint clean; errors wrapped with `%w`.
- [ ] Migration applies on a populated database and rolls back.
- [ ] No secret or infrastructure identifier committed; secret scan clean.
- [ ] End-to-end check: analyse a multi-speaker video locally and read the named speakers.
- [ ] Branch rebased on the integration branch, PR CI green.

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify
  quality findings, and re-review after changes. No merge without a passing review.

---

## G2: Recap page presents an analysed video as a report

**Outcome**

From an analysed video, the operator opens a recap page that reads like a report: a cover, a
recap of every speaker with speaking time and verdict counts, then one sheet per politician
listing the disputed and the credible statements with their justification and sources.

**Context**

Depends on G1 for speaker names and roles. The backend already serves a stored analysis and
streams SRT and CSV exports of it; the frontend has the analysis view but no report. The
reference report fixes the content and order:

1. Cover: brand lockup, tricolore rule, title, the participants, four key figures (duration,
   statements detected, credible, disputed), analysis date.
2. Recap: one row per principal with speaking time, share of principals' time, number of turns,
   statements detected, credible, disputed, unverifiable, not verified, and a distribution bar;
   a totals row; a legend; two notes, "how to read these figures" and "limits of this analysis".
3. One sheet per principal: the same figures, the distribution bar, then "disputed statements"
   and "credible statements", each with timestamp, flags, quote, justification and linked
   sources.

On the reference debate 127 of 226 principal statements could not be verified, and only
credible and disputed statements carried sources. The report is only honest if it shows that.

**Approach**

- One backend projection is the single source of the report: a recap read model computed in the
  service layer from the stored snapshot and the G1 speaker mapping, exposed by one endpoint.
  The page renders it and G3 exports it, so the two can never disagree. Speaking time comes
  from final transcript turns; verdict counts from the terminal result of each claim.
- Count four outcomes per speaker and never fold them together: credible, disputed,
  unverifiable, not verified (verification failed or was shed). Unverified statements are shown
  as counts, in the recap and on every sheet.
- Sheets cover speakers with role `principal`. Moderators and guests appear in the recap note
  with their speaking time, not as sheets. Default order is speaking time descending; the
  operator can reorder, and the order is part of the URL so it survives a reload and G3 reuses
  it.
- Quotes are verbatim transcript text. Truncate at a word boundary with an ellipsis; never
  rewrite. Sources are links.
- Reuse the brand tokens, the verdict palette and the verdict vocabulary already in the app; a
  verdict is never conveyed by colour alone. Use the existing display and text typefaces.
- Server Components by default; a client boundary only at the leaf that needs interaction.
  Copy goes through the existing dictionaries, French first.
- The page is responsive and is also the print surface G3 builds on: each of the three blocks
  is a self-contained section.
- The recap states its limits in plain words: share of unverified statements, automatic voice
  attribution, small counts that do not rank participants.

**Acceptance criteria**

* A completed analysis exposes a "recap" entry from the video; an analysis that is not
  complete does not.
* The page shows the cover, the recap and one sheet per principal, in that order, with the
  figures listed in Context.
* Totals on the recap equal the sum of the sheets, and equal the counts of the CSV export of
  the same analysis.
* Every disputed or credible statement shows its timestamp, flags, verbatim quote,
  justification and at least one working source link.
* Unverifiable and not-verified statements are counted on the recap and on every sheet.
* Unnamed speakers appear under their letter; the page does not invent a name.
* The operator can reorder the sheets and the order persists across a reload.
* The page is usable at phone width and passes the existing accessibility checks.
* A video with a single speaker, or with no verified statement, renders a coherent report
  rather than empty tables.

**Implementation todos**

- [ ] Recap read model in the service layer: per-speaker speaking time, turns, four outcome
  counts, statement list with justification, flags and sources; totals; limits figures.
- [ ] Endpoint returning the recap for a video, with the same access rule as the analysis read.
- [ ] Table-driven tests of the projection on synthetic snapshots, including overlapping turns,
  a claim with several results, a single-speaker video and an analysis with no verdict.
- [ ] Route and page for the recap; link from the analysed video.
- [ ] Cover, recap table and speaker sheet components, with the distribution bar and legend.
- [ ] Sheet ordering control backed by the URL.
- [ ] Dictionary entries for all copy, French and English.
- [ ] Component tests and a rendering test of the whole page from a fixture recap.
- [ ] Documentation page for the recap and how its figures are computed.

**Definition of Done**

- [ ] Versions and best practice verified before any new dependency.
- [ ] Go tests green under `-race`; frontend tests green.
- [ ] `gofumpt`, `go vet`, `golangci-lint` and ESLint clean; errors wrapped with `%w`.
- [ ] No secret or infrastructure identifier committed; secret scan clean.
- [ ] End-to-end check: open the recap of a locally analysed multi-speaker video and reconcile
  its totals with the CSV export.
- [ ] Branch rebased on the integration branch, PR CI green.

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify
  quality findings, and re-review after changes. No merge without a passing review.

---

## G3: Export the recap as a PDF

**Outcome**

On the recap page the operator presses one button and gets a PDF of the report: cover, recap,
then the speaker sheets, ready to send or print.

**Context**

Depends on G2: the PDF is the recap page's content on paper and reuses its read model and its
sheet order. The reference report was A4, 7 pages for five principals, typefaces embedded,
source links clickable. Producing it by hand showed the real difficulty: a sheet with eight or
nine statements does not fit one page at a readable size without a denser two-column layout,
and content that overflows silently collides with the footer.

**Approach**

- Decide the rendering route after verifying current best practice, and record the decision
  and its trade-offs: a print stylesheet driving the browser's own PDF output, client-side
  generation with a maintained library, or server-side rendering. Requirements that decide it:
  a real file download from one click, embedded typefaces, selectable text and working links
  (no rasterised pages), no headless browser added to the backend image without a written
  justification, and no new cost on the analysis path.
- The PDF is generated from the G2 recap read model, not from a second computation.
- A4 portrait. The cover and the recap are one page each. Each principal starts on a new page.
  A sheet that fits one page uses the single-column layout; a longer one switches to the dense
  two-column layout; if it still overflows it continues on a following page with its header
  repeated. Content is never clipped and never overlaps the footer.
- Every page after the cover carries the brand footer and a page number. The cover carries the
  analysis date. The file is named after the video and the analysis date.
- Print colours must hold in greyscale: verdicts keep their text label, bars keep a legend.

**Acceptance criteria**

* The recap page has an "export PDF" button; one press downloads a PDF file.
* The PDF contains the cover, the recap and the sheets in the order shown on the page.
* For the reference structure (five principals, between 4 and 9 sourced statements each) every
  sheet fits on one page and nothing is clipped.
* A sheet with more statements than one page holds continues on the next page with its header
  repeated.
* Text is selectable, source links are clickable, and the brand typefaces are embedded.
* The figures in the PDF equal those on the page for the same analysis.
* The button is unavailable, with a reason, while the recap is loading or the analysis is not
  complete.

**Implementation todos**

- [ ] Research note: rendering route chosen, alternatives rejected, versions.
- [ ] PDF layout: page geometry, cover, recap, speaker sheet in single-column and dense
  variants, footer and page numbers.
- [ ] Overflow rule: dense switch, then continuation page with repeated header.
- [ ] Export button on the recap page with loading, disabled and error states.
- [ ] File naming from the video title and the analysis date, sanitised.
- [ ] Automated test that renders a fixture recap to PDF and checks page count, the presence
  of every speaker name and of the totals, and that no page is empty.
- [ ] A fixture for the long-sheet case that exercises the continuation page.
- [ ] Documentation of the export and of the layout rules.

**Definition of Done**

- [ ] Versions and best practice verified; the rendering decision is recorded.
- [ ] Tests green (`-race` for any Go change); frontend tests green.
- [ ] Lint and format clean on every touched stack; errors wrapped with `%w`.
- [ ] No secret or infrastructure identifier committed; secret scan clean.
- [ ] End-to-end check: export the recap of a locally analysed multi-speaker video and read
  every page.
- [ ] Branch rebased on the integration branch, PR CI green.

**Code review (mandatory)**

- [ ] Run a code review on the diff, resolve every correctness finding, address or justify
  quality findings, and re-review after changes. No merge without a passing review.
