# Recap

The recap presents an analysed video as a report. It opens from the "Recap" link shown beside
the analysed chip of a video whose [pre-analysis](video-preanalysis.md) is complete, at
`/app/recap/{id}`. A video with no completed, readable analysis has no recap.

The page has three blocks, in this order:

1. **Cover.** Brand lockup, tricolore rule, the video title, the main speakers, four key figures
   (duration, statements detected, credible, disputed) and the analysis date.
2. **Recap.** One row per main speaker with speaking time, share of the main speakers' time,
   turns, statements detected, credible, disputed, unverifiable, not verified, and a distribution
   bar; a totals row; a legend; and two notes, "how to read these figures" and "limits of this
   analysis".
3. **Sheets.** One per main speaker: the same figures, the distribution bar, then the disputed
   and the credible statements, each with its timestamp, flags, verbatim quote, justification and
   linked sources.

Each block is a self-contained section, and the page is also the surface the PDF export prints.

## One projection

Every figure comes from one backend read model, served by `GET /api/videos/{id}/recap` (any
authenticated caller, the same access rule as the analysis read; `404` unknown video, `409` no
completed analysis, `500` when the stored analysis could not be read). The page renders it and the exports reuse it, so they cannot disagree. It
is computed on read from the stored analysis and the [speaker mapping](speaker-naming.md), and
nothing is stored.

## How the figures are computed

- **Speaking time and turns** come from the finalized transcript. A voice's speaking time is the
  sum of its own transcript segments, so the silence, the clips and the unattributed speech
  between two of them are not counted as its speech. Consecutive segments of one voice form one
  turn; another voice, labelled or not, ends it. When the transcriber attributes the same instant
  to two voices, each keeps its own time, so the speaking times can add up to more than the
  video's duration.
- **Share** is a main speaker's part of the main speakers' total speaking time.
- **Statements** are the claims the pipeline detected, each counted once at its final state. A
  later verdict for the same claim replaces the earlier one. Four outcomes are counted and never
  folded together:

  | Outcome | Meaning |
  |---------|---------|
  | Credible | Checked and supported by the cited sources |
  | Disputed | Checked and contradicted by the cited sources |
  | Unverifiable | Checked, with no evidence either way |
  | Not verified | Detected, but the check failed or did not run (verifier at capacity, error) |

  Detected is always the sum of the four. Only credible and disputed statements carry sources, so
  only they are listed on the sheets; unverifiable and not-verified statements are shown as
  counts, in the recap and on every sheet.
- **Who gets a sheet.** Voices with the `principal` role. The role comes from the speaker mapping
  when it covers the voice, and from the voice's speaking structure otherwise. Moderators and
  guests appear in the recap note with their speaking time, not as sheets.
- **Names.** A voice is printed under its resolved name, or under its diarized letter
  ("Speaker A") when it has none. The report never invents a name. A sheet says whether its name
  was attributed automatically or confirmed by an operator.
- **Quotes** are verbatim transcript text. A long quote is cut on a word boundary with an
  ellipsis and is never rewritten. A statement the pipeline could not anchor to exact words is
  quoted with the whole passage it was drawn from.
- **Timestamps** are the start of the transcript segment the quoted words sit in, or of the
  passage when the statement is not anchored.
- **Duration** is the media length when known, otherwise the end of the last transcribed
  segment.
- **The analysis date** is printed in the Europe/Paris time zone, on the page and in its
  exports alike.

## Reconciling the totals

- The totals row of the recap is the sum of the sheets: both cover the main speakers.
- The cover figures cover the whole analysis, main speakers plus every other voice. They equal the
  claim counters stored with the analysis and shown in the backoffice.
- They also equal the [CSV export](video-preanalysis.md#api) of the same analysis once it is
  reduced to the last row of each `claim_id`: the CSV is a decision trace and keeps one row per
  verdict, so a claim whose verdict was upgraded has two rows there and counts once here.

## Sheet order

Sheets are ordered by speaking time, longest first. The operator can move a sheet up or down
with the "sheet order" control. The order is written to the URL (`?order=B,A,C`), so it survives
a reload, travels with a shared link, and is the order the PDF export uses. A label the URL
names that is no longer a main speaker is ignored, and a main speaker the URL omits follows in
the default order: an outdated link never hides a sheet.

## Limits the report states

The recap prints its own limits beside the figures:

- the share of the main speakers' statements with no usable verdict (unverifiable plus not
  verified). On the reference debate this was 127 of 226;
- that voices are attributed automatically, with the number of names proposed by the analysis,
  confirmed by an operator, and missing;
- that the counts are small and describe one debate: they do not rank the speakers.
