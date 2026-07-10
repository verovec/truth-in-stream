# PDF fact-check (Documents)

Fact-checking extends beyond live streams to documents people read. An admin uploads a PDF (a press
article, report, or official publication); the same retrieve-then-verify pipeline that scores live
transcripts analyses its sentences once and persists the results in Postgres. Any authenticated user
then opens the document in an embedded viewer where credible and disputed sentences are highlighted
in place.

This is the Documents surface (`/documents`). For the live-stream path, see
[How it works](../README.md#how-it-works); for the verify-path settings this feature depends on, see
[Configuration](configuration.md).

## The flow, end to end

```
  Admin uploads a PDF          /documents, admin only
     |
     v
  Browser text extraction      pdf.js reads the text layer, normalizes it (NFKC,
     |                         de-hyphenation, whitespace collapse) and segments it
     |                         into sentences (Intl.Segmenter, French-aware)
     v
  POST extraction              the browser posts {page_count, sentences} to the backend,
     |                         which stores them and flips the document `ready`
     v
  Analysis (verify path)       each stored sentence runs through the existing VerifyPath -
     |                         gate, decompose into atomic claims, retrieve, curated
     |                         fast-match or LLM verify - with NO load-shedding
     v
  Results persisted            per-sentence verdicts land in Postgres as they resolve
     |
     v
  In-PDF viewer                /documents/[id]: any authenticated user reads the PDF with
                               credible/disputed sentences highlighted; hover shows the
                               verdict, click syncs the side panel
```

Text is extracted **in the browser** with pdf.js - the same engine the viewer uses to render the
PDF - so highlight anchoring is deterministic and does not drift on ligatures, hyphenation, or
whitespace. A scanned PDF with no extractable text layer is rejected client-side before anything
reaches the server; there is no OCR path.

The analyzer reuses the live pipeline unchanged, with one deliberate divergence: **no
load-shedding**. Live marks a claim `not_checked` when the verify pool saturates because realtime
cannot wait; a document job can wait, so the analyzer applies backpressure and blocks until a verify
slot frees. Every sentence therefore gets a real outcome. When `FACTCHECK_POLITICAL` is active, the
political two-axis verdicts apply automatically, exactly as on the live path.

## Access model

The surface is authenticated end to end - `/documents` and `/documents/[id]` are not public routes,
and the whole `/api/documents/*` subtree sits behind the Keycloak identity gate. Within that gate,
mutating actions additionally require the `admin` role:

| Action | Route | Who |
|--------|-------|-----|
| Upload a PDF | `POST /api/documents/uploads` | admin |
| Post the browser extraction | `POST /api/documents/{id}/extraction` | admin |
| Reanalyse | `POST /api/documents/{id}/reanalyse` | admin |
| Delete | `DELETE /api/documents/{id}` | admin |
| List the library | `GET /api/documents` | any authenticated user |
| View metadata + PDF URL | `GET /api/documents/{id}` | any authenticated user |
| Read sentences + verdicts | `GET /api/documents/{id}/claims` | any authenticated user |

An admin-only request without the role gets `403`; an unauthenticated request gets `401`. A `guest`
user can browse and read every document but cannot upload, reanalyse, or delete.

## Analysis and the verify-path dependency

Analysis requires the **verify path** to be configured (`FACTCHECK_VERIFY_PATH` active with its
Anthropic key). Upload, extraction, listing, and viewing all work regardless of the verify path:
extraction still stores sentences and flips the document `ready`, but when the verify path is off it
skips the auto-start and leaves `analysis_status = none`. Only starting an analysis (the auto-start
after extraction, and `reanalyse`) surfaces a clear "analysis is disabled" error when the verify path
is not configured.

In local development the document analyzer's behaviour is governed by the `FACTCHECK_VERIFY_*` (and
`FACTCHECK_POLITICAL`) variables that `docker-compose.yml` forwards to the backend. A variable set in
`.env` does nothing unless Compose passes it through, so with the verify path off, uploads and the
viewer work but analysis stays disabled. See
[Configuration -> PDF documents](configuration.md#pdf-documents).

A document moves through an `analysis_status` lifecycle: `none` (extracted, not yet analysed) ->
`analysing` -> `complete`, or `failed` with an error message. The analyzer runs in-process as a
background job. Because that job dies with the backend process, any document left `analysing` when
the backend restarts is flipped to `failed` ("interrupted by restart") on startup recovery; the admin
reanalyses it with one click.

## Reanalysis semantics

Verdicts must reflect the **current** claims database, so an admin can reanalyse a document at any
time. Reanalysis re-runs the pipeline over the sentences extracted at upload (they are stored once
and reused) and keeps only the latest verdict per sentence. Starting a run flips the document to
`analysing`, zeroes the progress counters, and clears any prior error, but it does **not** wipe the
previous verdicts up front: each sentence's prior claims and skip reason are replaced only as that
sentence is reprocessed. A run that fails partway therefore leaves already-processed sentences with
their fresh results and the rest with the previous run's - never an empty document. There is no
analysis history. `POST .../reanalyse` returns `202` and proceeds in the background; a document that
is already `analysing` returns `409`, the lock that prevents concurrent runs.

## The viewer

`/documents/[id]` is a two-pane layout: the PDF on the left, a fact-check side panel on the right.

- **Highlights.** Credible sentences are shaded translucent emerald and disputed ones translucent
  rose - the verdict-badge palette, so the underlying text stays readable. Only credible and disputed
  sentences are highlighted in the PDF; unverifiable and skipped sentences are not shaded but still
  appear in the side panel. A sentence that fails to anchor in the rendered PDF (a rare edge) still
  shows in the panel - an anchoring miss never hides a result.
- **Interactions.** Hovering a highlight shows a compact tooltip (the verdict badge and a claim
  snippet); clicking selects the sentence and scrolls the side panel to its full verdict card with
  sources. The panel lists every analysed sentence in document order - including unverifiable and
  skipped ones - and clicking a panel row scrolls the PDF to that sentence. Selection is
  bidirectional.
- **Progress.** While a document is `analysing`, the page polls the persisted state every couple of
  seconds; a progress bar tracks `sentences_processed / sentences_total` and highlights appear as
  verdicts land. Because progress is read from persisted state, the page is refresh-safe. Admins see a
  Reanalyse button (disabled while a run is in flight); a `failed` document shows its error with a
  retry action.

## Configuration knobs

The document API caps are documented with the rest of the backend configuration; see
[Configuration -> PDF documents](configuration.md#pdf-documents). In short: `DOCUMENT_MAX_SIZE_BYTES`
(default 30 MB) caps the upload size, `DOCUMENT_MAX_SENTENCES` (default 1500) caps how many sentences
one document may submit, and `DOCUMENT_ANALYSIS_TIMEOUT` (default 30 minutes) bounds one analysis run.
All three bound LLM cost and abuse and keep the backend defaults when unset.
