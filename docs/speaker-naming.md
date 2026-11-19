# Speaker naming

The live pipeline labels voices `A`, `B`, `C`... as the streaming transcriber diarizes them.
Once a [video pre-analysis](video-preanalysis.md) completes, a post-analysis step maps each
letter to a person: a name, a role, a confidence level and the transcript evidence the name was
inferred from. An operator can correct or confirm any of them.

The stored analysis is never rewritten. Its frames keep the diarized letters; names live in
their own table (`video_speakers`, one row per video and label) and are applied wherever an
analysis is read: the analysis view, the speaker reliability panel, and the SRT and CSV exports.
A voice with no name falls back to its letter everywhere.

## How a name is decided

Deterministic signals first, a generative call last.

1. **Turns.** The finalized transcript segments of the stored analysis are merged into speaker
   turns (consecutive segments of one label form one turn).
2. **Participants.** A model lists the people named in the video title and in the name-bearing
   parts of the transcript (the opening turns, then a window of words around every capitalised
   word elsewhere). Each name must come with a quoted span. A name is kept only when that quote
   is an exact substring of the title or of a turn and contains the name; anything else is
   discarded, so nothing comes from model memory. A name is stored as it was heard: a guest
   introduced by first name only keeps the first name.
3. **Signals.** Every mention of a kept name credits a (label, name) pair:

   | Signal | When | Credits | Weight |
   |--------|------|---------|--------|
   | Self-reference | The speaker introduces themself ("je suis ...", "je m'appelle ...", "my name is ...") | the speaker | 4 |
   | Handoff | The name sits in the last words of a turn ("Madame Durand, vous avez deux minutes.") | the next voice | 3 |
   | Closing | The name follows a thank-you ("Merci Monsieur Durand.") | the previous voice | 1 |
   | Reply | The name opens a longer turn ("Madame Durand, ce que vous dites...") | the previous voice | 1 |
   | Vocative adjacency | Any other mention | both neighbouring voices | 1 |

   The speaker of a mention is never credited for merely uttering a name. Diarization bleeds a
   moderator's sentence into a candidate's turn when they overlap, which makes raw utterance
   counts misleading.
4. **Fusion.** Each name goes to at most one label and each label gets at most one name,
   strongest pair first. Confidence is the pair's lead over its strongest rival (the best other
   name for that label, or the best other label for that name), scaled down while the evidence is
   thin: a single handoff alone never suffices. A label below the confidence floor (0.6) stays
   unnamed. A wrong name on a statement is worse than a letter.
5. **Tie-break.** When a label is left open between a few close candidates, the model is asked
   which one it is, from that label's own turns. Its answer is accepted only if the name is one of
   the candidates, no other label holds it, and the quoted span is an exact substring of one of
   that label's own turns.
   An accepted tie-break sits exactly at the confidence floor.
6. **Roles** come from structure alone: a `principal` holds long turns and most of the speaking
   time, a `moderator` speaks less and in short turns, a `guest` is marginal and occasional. A
   transcript with no usable timing yields `unknown`.

The proposal stored for each label carries the name (or none), the role, the confidence, the
method (`signals`, `tie_break`, `operator` or `none`) and up to five quoted spans with their
video time.

## When it runs

- **Automatically**, after a pre-analysis is stored as complete, off the analysis path: it runs
  on its own goroutine and its own time budget once the run has released its slot. A failure is
  logged and leaves the analysis complete and its speakers unnamed; it never fails or delays the
  analysis. `SPEAKER_NAMING_ENABLED=false` turns the automatic run off.
- **On demand**, from the "re-run identification" control of the naming panel or
  `POST /api/videos/{id}/speakers/name`. A re-run replaces the step's previous proposals. If it
  fails, nothing is written and the previous names stay. The call answers synchronously within
  55 seconds. If only a tie-break call fails, the run still completes and a name an earlier run
  settled by tie-break for that voice is kept.

A session that watched an analysis finish re-reads the mapping for up to two minutes, so the
names appear without a reload once the step has written them.

Storing a new analysis clears the names proposed for the previous one in the same transaction:
the transcriber assigns labels afresh on every run, so a name inferred for the old `A` never
labels the new one. The step then proposes again. Operator-confirmed speakers are kept.

Without a key for the selected `LLM_PROVIDER`, the step still runs and classifies roles, and
every voice stays unnamed until an operator names it.

## Operator corrections

Under the player of an analysed video, the naming panel lists each voice with its name, role,
confidence and evidence. An admin can type a name, pick a role and confirm. Confirming without
editing accepts the proposal as it stands; confirming an empty name records that the voice is
deliberately unnamed.

A confirmed speaker is marked operator-set and is never overwritten by a later run of the step.

## API

| Endpoint | Access | Behaviour |
|----------|--------|-----------|
| `GET /api/videos/{id}/analysis` | any authenticated | Carries `speakers` beside `frames` once a completed result is readable. |
| `GET /api/videos/{id}/speakers` | any authenticated | The stored mapping; an empty list when the step has not run. |
| `POST /api/videos/{id}/speakers/name` | admin | Re-run the step and return the mapping. `404` unknown video; `409` no stored analysis; `502` the step failed and the previous names are unchanged. |
| `PATCH /api/videos/{id}/speakers/{label}` | admin | Body `{"name": "...", "role": "principal\|moderator\|guest\|unknown"}`. Sets and confirms one speaker. `400` invalid role or a name over 120 characters; `404` unknown video or a label absent from the analysis; `409` no stored analysis. |

Each speaker is `{label, name, role, confidence, method, evidence: [{kind, quote, at}], operator_set}`;
`at` is the video time of the quoted turn in seconds.

Exports: the SRT prefixes each cue with the resolved name, or the letter when there is none. The
CSV keeps the `speaker` column (the letter) and adds `speaker_name` as its last column, so the
positions of the existing columns do not move.

## Configuration

| Variable | Default | Purpose |
|----------|---------|---------|
| `SPEAKER_NAMING_ENABLED` | `true` | Run the step automatically after each completed pre-analysis. The on-demand re-run and the operator corrections work either way |
| `SPEAKER_NAMING_MODEL` | provider default | Model used to list participants and break ties |
| `SPEAKER_NAMING_API_KEY` | empty | Anthropic key, used only when `LLM_PROVIDER=anthropic`. The other providers read their global key (`DEEPSEEK_API_KEY`, `GEMINI_API_KEY`) |

## Limits

- **Names must be spoken or in the title.** A participant nobody names stays a letter.
- **Self-reference is pattern-based.** Only explicit self-introductions are detected
  deterministically; a biographical statement only one participant could make is recognised only
  through the tie-break.
- **Long programmes are sampled.** The participant call sends a bounded excerpt (about 24,000
  characters) of the name-bearing parts of the transcript, not the whole of it.
- **Shared surnames.** A single word stands for a full name only when no other participant
  shares it; two participants with the same surname resolve only when addressed by full name.
- **Diarization is taken as given.** If the transcriber splits one person across two labels, at
  most one of them is named; if it merges two people under one label, the label is likely to stay
  unnamed.
- **Re-analysis keeps confirmations.** Proposed names are cleared when a video is re-analysed,
  operator-confirmed ones are not. Labels are assigned by the transcriber on each run, so after
  re-analysing a video check its confirmed speakers: a letter can designate a different voice in
  the new run.
