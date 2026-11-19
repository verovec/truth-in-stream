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

## PDF export

The "Export PDF" button of the recap page downloads the report as a PDF file in one press:
the cover, the recap, then the sheets in the order the page shows them. The file is named after
the video and the analysis date, for example `le-grand-debat-2026-11-19.pdf`. The button is
disabled, with the reason beside it, while the recap is loading, when the analysis is not
complete, and when the recap could not be loaded.

The PDF is generated in the browser from the recap object already on screen and the sheet order
in the URL. Nothing is recomputed and the backend is not involved, so the figures in the file
are the page's.

### Layout rules

- A4 portrait, 40 pt margins, with a bottom band reserved for the footer on every page after the
  cover. Content never enters that band.
- The cover and the recap are one page each. The cover carries the analysis date.
- Each main speaker's sheet starts on a new page.
- A sheet whose statements are estimated to fit one page under its header uses a single
  readable column (10 pt). A longer one switches to the dense layout: two columns at 8.5 pt,
  split near the middle in reading order, never right after a section heading. On the reference
  structure (five main speakers, 4 to 9 sourced statements each) every sheet fits one page.
- A sheet that still does not fit continues on the next page, with the speaker's name repeated
  at the top ("Name (continued)"). A statement is kept whole on its page; only one too long to
  fit a page at all (an unusually long justification) may continue on the next.
- The layout choice counts everything the page must hold: the header, a name long enough to
  wrap, the statements, and the note counting the statements the sheet does not detail.
- A source with no title prints its host name; a run of characters too long to be a word, such
  as a URL inside a quote, may break rather than run out of its column.
- Every page after the cover has the brand footer and "Page N / total".
- Text is selectable, source links are clickable, and the two brand typefaces are embedded
  (Geist for text, Spectral for titles). Words are never hyphenated: quoted words are not broken
  with another language's rules. A title or a name with a character the Spectral file cannot draw
  (it is the Latin subset) is set whole in Geist, which covers extended Latin, never printed with
  missing letters. The typographic characters neither file draws are mapped to ones both do: the
  narrow no-break space becomes a no-break space, the Unicode hyphens a plain hyphen, and the
  soft hyphen is dropped.
- The report holds in greyscale: verdicts keep their text label, the distribution bar keeps its
  legend, and the four outcomes are told apart by tone as well as hue.

### Rendering route

Decided 2026-11-19 after checking current documentation and releases.

| Route | Outcome |
|-------|---------|
| `@react-pdf/renderer` 4.9.0, in the browser, loaded on click | **Chosen.** A real file from one press, selectable text, clickable links, embedded fonts, automatic page breaks with repeated elements and page numbers. Declares React 19 support. No server cost |
| Print stylesheet and the browser's own PDF output | Rejected: it opens the print dialog, so one press does not produce a file, and the page geometry depends on the reader's browser settings |
| `pdf-lib` | Rejected: no layout engine; wrapping, pagination, repeated headers and page totals would all be hand-written |
| `jsPDF` with its table plugin | Rejected: manual text layout; keeping a statement together while repeating a sheet header is fragile |
| `pdfmake` | Workable fallback (headers, footers, page breaks, embedded fonts), less typed and not React-shaped |
| Server-side rendering | Rejected: a headless browser in the backend image, or a second layout implementation in Go, for no gain over the browser route |

Trade-offs and things to know:

- **Weight.** The library is about 440 kB gzipped. It and the PDF document are imported only when
  an export is requested; the recap page does not load them.
- **Fonts.** `next/font` serves the typefaces to the page but exposes no file a PDF generator can
  read. The static files come from the pinned `geist` (1.9.2) and `@fontsource/spectral` (5.3.0)
  packages, both under the SIL Open Font License, and are copied into `public/fonts/recap/` by
  `scripts/copy-recap-fonts.mjs` before `dev` and `build`. The copies are build artifacts and are
  not committed. Spectral is the `latin` subset, which covers French. On the first export the
  page downloads the files itself and registers them as data, only once all have arrived: the
  library caches a failed font download for good, so letting it fetch them would turn one network
  error into a broken export until the page is reloaded.
- **No multi-column flow.** The library has no column layout that flows by itself; the dense
  variant is a row of two columns whose split is decided before rendering from estimated
  heights. Each column continues on the next page independently when a sheet overflows.
- **Library quirks found in 4.9.0**, all covered by the rendering test: a line height set on a
  page makes per-page texts (page number, continuation header) disappear, so the document sets
  none; a per-page text needs both its left and right edges set to get a width; and laying out
  a no-break space in the Spectral file throws, so titles and names are set with plain spaces
  (running text, in Geist, keeps its no-break spaces).
- **Trusted characters.** The set of characters set in Spectral was read from the font file
  itself, not from the subset's declared range, which is wider than what the file holds. A test
  renders every one of them. Re-read it when `@fontsource/spectral` is upgraded.
- **Verification.** `recap-document.test.tsx` renders fixture recaps to real PDF bytes in Node
  and reads them back with pdf.js: page count, every speaker name, the totals, the links, the
  embedded fonts, and the continuation of a thirty-statement sheet.

## Limits the report states

The recap prints its own limits beside the figures:

- the share of the main speakers' statements with no usable verdict (unverifiable plus not
  verified). On the reference debate this was 127 of 226;
- that voices are attributed automatically, with the number of names proposed by the analysis,
  confirmed by an operator, and missing;
- that the counts are small and describe one debate: they do not rank the speakers.
