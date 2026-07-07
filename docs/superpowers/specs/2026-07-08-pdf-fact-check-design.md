# PDF Fact-Check - Design

Date: 2026-07-08
Status: Approved (design review with owner)
Scope: PDF document upload, persistent fact-check analysis, reanalysis, and an in-PDF
highlight viewer. Backend (`stack/backend`), frontend (`stack/frontend`), local compose.
Out of scope: OCR for scanned PDFs, analysis version history, non-PDF document formats,
public (unauthenticated) access, prod infrastructure changes beyond existing services.

## Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Access | Admins upload, reanalyse, delete; every authenticated user views | Matches curated ingestion; avoids abuse and unbounded LLM cost |
| Processing UX | Live progress; highlights appear as claims resolve; refresh-safe | Same feel as the live page without realtime infra |
| Highlights | Credible and disputed only; side panel lists every analysed sentence | Consistent with the video fact-check section, which drops invérifiable |
| OCR | Text-layer PDFs only; scanned PDFs rejected client-side before upload | Born-digital documents cover the use case; OCR is its own epic |
| Reanalysis | Replace previous results, keep latest only | Results must reflect the current claims database; history is YAGNI |
| Text extraction | Browser-side with pdf.js at upload, persisted to Postgres | Same engine as the viewer, so highlight anchoring is deterministic; Go PDF extractors risk whitespace/hyphenation/ligature drift that silently breaks substring matches |
| Progress transport | Poll persisted state every ~2s | Analysis is finite and persisted; polling the persisted truth is refresh-safe with zero new infra |
| Analysis executor | In-process background goroutine (YouTube-ingest pattern) with startup recovery | Admin-triggered, single-instance backend; a queue worker adds a prod deploy surface for no benefit |
| Verify semantics | No load-shedding; block on verify-pool backpressure | A batch job can wait; every sentence gets a real outcome, unlike live's `not_checked` shedding |
| PDF viewer | `react-pdf@10.4.1` (pins `pdfjs-dist@5.4.296`); hand-rolled highlight overlay | Highlighter libraries are pinned to React 18 / pdfjs 4.x and model user-drawn selections, not programmatic sentence anchoring |
| Sentence segmentation | `Intl.Segmenter("fr", { granularity: "sentence" })` | Built-in, no dependency, French-aware |

## 1. Goal

Extend fact-checking beyond live streams to documents people read: an admin uploads a
PDF (press article, report, official publication), the existing retrieve-then-verify
pipeline analyses its sentences once, and the results persist in Postgres. Any
authenticated user opens the document in an embedded viewer where credible and disputed
sentences are highlighted in place; hovering shows the verdict, clicking reveals the full
verdict card with sources. Admins can reanalyse a document at any time so verdicts
reflect the current claims database.

## 2. Storage layout (S3)

One folder per document under a dedicated prefix, leaving room for future derived assets:

```
documents/
  {documentId}/
    original.pdf
```

Existing prefixes (`uploads/`, `youtube/`) are untouched. Same bucket, same
`storage.S3Store` presign machinery as videos. The storage port gains a `Delete` method
(it has none today) so document deletion removes the object.

## 3. Data model (migration `0012_documents`)

**`documents`** - one row per PDF:

| Column | Type / constraint |
|---|---|
| `id` | `uuid PK default gen_random_uuid()` |
| `title` | `text NOT NULL` |
| `object_key` | `text UNIQUE NOT NULL` (`documents/{id}/original.pdf`) |
| `content_type` | `text NOT NULL` (`application/pdf`) |
| `size_bytes` | `bigint NOT NULL` |
| `page_count` | `int NOT NULL DEFAULT 0` |
| `status` | `text CHECK (pending\|ready\|failed)` - upload lifecycle, mirrors `videos` |
| `analysis_status` | `text CHECK (none\|analysing\|complete\|failed)` |
| `analysis_error` | `text` |
| `sentences_total` / `sentences_processed` | `int NOT NULL DEFAULT 0` - progress counters |
| `analyzed_at` | `timestamptz` |
| `analysis_runs` | `int NOT NULL DEFAULT 0` |
| `created_at` / `updated_at` | `timestamptz NOT NULL DEFAULT now()` |

**`document_sentences`** - extracted once at upload, reused by every reanalysis:

| Column | Type / constraint |
|---|---|
| `document_id` | `uuid FK -> documents ON DELETE CASCADE` |
| `seq` | `int NOT NULL`, `UNIQUE(document_id, seq)` - document order |
| `page` | `int NOT NULL` - 1-based |
| `text` | `text NOT NULL` - normalized sentence text |
| `occurrence` | `int NOT NULL DEFAULT 1` - nth identical string on that page, disambiguates anchoring |
| `skip_reason` | `text` - `not_a_claim` / `not_covered` / empty; rewritten each analysis run |

**`document_claims`** - verdicts; wiped and rewritten on reanalysis:

| Column | Type / constraint |
|---|---|
| `id` | `uuid PK` |
| `document_id` | `uuid FK -> documents ON DELETE CASCADE` |
| `sentence_seq` | `int NOT NULL` - joins `document_sentences(document_id, seq)` |
| `claim_id` | `text NOT NULL` - pipeline correlation id |
| `text` | `text NOT NULL` - atomic claim text |
| `status` | `text CHECK (verified\|error)` |
| `source` | `text CHECK (curated\|verified)` |
| `verdict` | `text CHECK (credible\|disputed\|unverifiable)` |
| `basis` | `text CHECK (evidence\|knowledge)` |
| `literal` | `text NULL CHECK (accurate\|inaccurate\|unverifiable)` - political axis |
| `flags` | `text[]` - manipulation flags, political axis |
| `confidence` | `float8` |
| `rationale` | `text` |
| `citations` | `jsonb` - `[]domain.SegmentMatch`, same shape as the live wire format |
| `created_at` | `timestamptz` |

No `unchecked` claim status: the analyzer blocks instead of shedding, so a claim either
verifies or errors.

## 4. API surface

All routes sit under the identity gate; mutating routes additionally require the
Keycloak `admin` role.

| Route | Role | Behaviour |
|---|---|---|
| `POST /api/documents/uploads` | admin | Validates `application/pdf` + size cap, creates a `pending` row, returns `{documentId, objectKey, upload: PresignedRequest}` - mirrors the video ticket flow |
| `POST /api/documents/{id}/extraction` | admin | Body `{page_count, sentences: [{seq, page, text, occurrence}]}`. Validates non-empty, sentence cap, S3 object exists. Stores sentences, flips `ready`, auto-starts the first analysis when the verify path is enabled. |
| `POST /api/documents/{id}/reanalyse` | admin | 202; wipes claims and skip reasons, zeroes counters, re-runs over stored sentences. 409 if already `analysing`. |
| `GET /api/documents` | any | Library list: metadata, statuses, progress, verdict summary counts |
| `GET /api/documents/{id}` | any | Metadata, progress, presigned GET for the PDF |
| `GET /api/documents/{id}/claims` | any | Sentences joined with claims - the polling target |
| `DELETE /api/documents/{id}` | admin | Deletes rows (cascade) and the S3 object |

Config knobs: `DOCUMENT_MAX_SIZE_BYTES` (default 30 MB), `DOCUMENT_MAX_SENTENCES`
(default 1500). Both bound LLM cost and abuse.

## 5. Backend analysis

Layering mirrors the video domain exactly:

- `internal/domain/document.go` - `Document`, `DocumentSentence`, `DocumentClaim`
  models and the `DocumentStore` port.
- `internal/service/document.go` - `DocumentService`: tickets, extraction ingest and
  validation, list/get/delete.
- `internal/service/document_analyzer.go` - the background job, `spawn`-injected
  goroutine like `IngestService`.
- `internal/handler/documents.go` - HTTP only; routes registered in `handler.NewMux`.
- `queries/documents.sql` -> sqlc regen -> `internal/store/postgres` mapping methods.

### Pipeline reuse

The analyzer feeds each stored sentence through the existing `VerifyPath` as one
analysis unit: gate -> decompose into atomic claims -> retrieve -> curated fast-match or
LLM verify. The political two-axis path applies automatically when `FACTCHECK_POLITICAL`
is active. No matcher, gate, or verifier code is duplicated; the analyzer consumes the
per-claim progressive results (the same events the live handler turns into WebSocket
frames) and persists each as a `document_claims` row as it resolves.

One deliberate divergence from live: no load-shedding. Live marks claims `unchecked`
when the verify pool saturates because realtime cannot wait. A document job can wait, so
the analyzer applies backpressure and blocks until a verify slot frees. Concurrency is
bounded by the existing verify-pool configuration.

### Job lifecycle

- `analysis_status = analysing` is the lock; a concurrent reanalyse gets 409.
- Job start, one transaction: delete `document_claims`, clear `skip_reason`s, zero
  counters, set `analysing`.
- Per completed sentence: bump `sentences_processed`, write skip reason or claim rows.
- Completion: `complete`, `analyzed_at = now()`, `analysis_runs + 1`. Failure: `failed`
  plus `analysis_error`.
- Startup recovery: on boot, rows stuck in `analysing` flip to `failed`
  ("interrupted by restart"); the admin reanalyses. This is the accepted trade-off of
  the in-process executor.
- The feature requires `FACTCHECK_VERIFY_PATH` active for analysis. Upload, extraction,
  list, and view work regardless: extraction still stores sentences and flips the
  document `ready`, but skips the auto-start and leaves `analysis_status = none`.
  Only `reanalyse` (and the auto-start) surface a clear "analysis disabled" error when
  the verify path is not configured. Compose must forward the verify-path variables
  to the backend for local e2e (see the env passthrough gotcha: a variable in `.env`
  does nothing unless `docker-compose.yml` forwards it).

## 6. Frontend

### Routes and navigation

- `/documents` - library: document list plus admin upload. `/documents/[id]` - viewer.
  Both are auth-gated automatically (not in `PUBLIC_PATHS`).
- The inline `/app` header is extracted into a shared `AppHeader` carrying the product
  app's first navigation: Videos (`/app`) | Documents (`/documents`). Conventions kept:
  English chrome, French verdict vocabulary, zinc surfaces, sky selection/focus, no
  dictionary wiring.

### Upload flow (admin, extract-first)

1. Browser opens the file with pdf.js (the `pdfjs-dist` pinned by react-pdf) and runs
   `getTextContent` per page - no canvas render.
2. Page text is normalized exactly as the viewer will normalize at anchor time: NFKC
   (ligatures, diacritics), de-hyphenation of line-broken words, whitespace collapse.
3. `Intl.Segmenter("fr", { granularity: "sentence" })` segments sentences; each carries
   `{seq, page, text, occurrence}`.
4. A PDF with no extractable text (scanned) is rejected client-side with a clear
   message before anything reaches the server.
5. Then: `POST /api/documents/uploads` -> presigned PUT via the existing
   `putWithProgress` -> `POST extraction` -> redirect to the viewer, analysis running.
   A new `useDocumentUploads` hook mirrors the `useVideoUploads` state machine
   (requesting -> uploading -> extracting -> confirming -> ready/error).

### Viewer page

Two-pane layout like the live page (`lg:grid-cols-[minmax(0,1fr)_22rem]`): PDF left,
fact-check panel right.

- **Viewer**: `react-pdf@10.4.1`, all pages in a scrollable column. Loaded via
  `next/dynamic` with `ssr: false` (pdf.js touches `DOMMatrix`). Worker copied to
  `public/pdf.worker.min.mjs` (Turbopack-safe; `output: standalone` ships `public/`
  automatically); `TextLayer.css` and `AnnotationLayer.css` imported so text-layer spans
  align with the canvas.
- **Highlight overlay** (hand-rolled): per page, an absolutely-positioned layer above
  the text layer. Anchoring uses the pdf.js find-controller technique: concatenate the
  page's normalized text items into one string with a char-offset -> item-index map,
  substring-match each stored sentence (using `occurrence` to disambiguate duplicates),
  resolve the match to the rendered text-layer spans, read their rects, and merge
  contiguous same-line rects into one box per visual line. Credible = translucent
  emerald, disputed = translucent rose - the badge palette, text stays readable.
  Sentences that fail to anchor (rare edge) still appear in the side panel; anchoring
  failures never hide results.
- **Interactions**: hover shows a compact tooltip (verdict badge + claim snippet);
  click selects the sentence and scrolls the side panel to its full verdict card.
  The side panel lists every analysed sentence in document order - including
  invérifiable and skipped ones - reusing `VerifiedClaim`, `MatchRow`, and the
  `LIVE_ROW` selection classes; sources render as the existing sky underlined external
  links. Clicking a panel row scrolls the PDF to its highlight. Bidirectional selection
  with the same sky emphasis as the live page.
- **Progress**: while `analysing`, poll `GET /api/documents/{id}` +
  `/claims` every ~2s (plain `fetch` + `AbortController`, injection-seam props - no
  data-fetching library, per app convention). Progress bar shows
  `sentences_processed / sentences_total`; highlights appear as claims land. Admins get
  a Reanalyse button (confirm dialog, disabled while running); `failed` shows
  `analysis_error` with a retry CTA.

## 7. Epic breakdown

```
A --> B                (backend chain)
A --> C --> D --> E    (frontend chain; D also needs B for a real e2e)
```

| Card | Scope | Depends on |
|---|---|---|
| A - Backend documents domain | Migration `0012`, sqlc queries + regen, store mapping, domain models, `DocumentService`, storage `Delete`, handler routes, config caps | - |
| B - Document analyzer | `document_analyzer.go`, VerifyPath reuse with backpressure, job lifecycle + transactions, reanalyse endpoint, startup recovery, compose env forwarding | A |
| C - Documents library page | `react-pdf` dep + worker setup, extraction + normalization module (`lib/pdf/`), `Intl.Segmenter` segmentation, `useDocumentUploads`, `/documents` page, `AppHeader` nav extraction | A |
| D - Document viewer page | `/documents/[id]`, react-pdf viewer, polling store, progress bar, side panel with all analysed sentences, reanalyse button, failure states | C, B |
| E - Sentence highlights | `lib/pdf/anchor.ts` offset-map matcher (table-driven tests over hyphenation/ligature/multi-item cases), per-page overlay, hover tooltip, click <-> panel bidirectional selection | D |

Cards A/B and A/C/D/E overlap on files within their chains, so the chains are hard
dependencies; B and C run in parallel after A. Every card carries tests with the change,
an e2e check, and the mandatory code-review gate. Documentation (README, `docs/`) is
revisited once the epic completes.

## 8. Testing

- Go: table-driven unit tests per layer (`-race`); service tests fake the store and the
  verify path; analyzer tests cover lifecycle transitions, wipe-and-rewrite
  transactionality, startup recovery, backpressure (no shedding), and the 409 lock.
- Frontend: colocated Vitest. The normalization and anchoring modules are pure
  functions with table-driven cases (hyphenation, ligatures, multi-item sentences,
  duplicate sentences via `occurrence`). Components test via injection-seam props;
  upload hook state machine tested like `useVideoUploads`.
- E2E per card through the real entrypoints (compose stack, real endpoints, real
  browser flow for upload/viewer), not the internals underneath.

## 9. Risks and mitigations

- **Anchoring misses**: normalization drift between extraction and render is the main
  failure mode; mitigated by same-engine extraction, one shared normalization module,
  and the rule that unanchored sentences still show in the side panel.
- **Interrupted analysis**: in-process executor dies with the process; mitigated by
  startup recovery to `failed` plus one-click reanalyse.
- **LLM cost**: bounded by `DOCUMENT_MAX_SENTENCES`, the sentence gate (most sentences
  are not check-worthy), the curated fast-match path, and admin-only triggering.
- **react-pdf/Turbopack worker**: known asset-URL bug; mitigated by serving the worker
  from `public/`.
