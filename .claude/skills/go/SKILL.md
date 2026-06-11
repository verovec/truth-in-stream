---
name: go
description: Use when writing, reviewing, or refactoring any Go code in stack/backend - HTTP handlers, services, stores, sqlc queries, migrations, tests, benchmarks, or lint config
---

# Go backend (stack/backend)

Module `github.com/verovec/truth-in-stream/backend`. Stdlib `net/http` service - no framework, no router library, no DI container, ever. Entry point `cmd/server/main.go` with graceful shutdown and `/healthz`.

These rules are MUST/NEVER, not suggestions. "It works" is not the bar; the bar is the most maintainable, allocation-conscious code that could carry this service for years. If a rule blocks you, say so in the PR - do not quietly deviate.

## Toolchain

Go 1.26 (go.mod `go 1.26`; local and CI on 1.26.x). Use the modern APIs - reaching for the pre-1.22 equivalent is a review finding:

| Use | Never |
|---|---|
| `log/slog` (JSON handler) | `log`, `fmt.Println` for logging |
| `math/rand/v2` | `math/rand` |
| `errors.AsType[T]` (1.26, allocation-free) | `errors.As` in new code |
| `for i := range n` | `for i := 0; i < n; i++` |
| `slices` / `maps` packages | hand-rolled loops for contains/sort/clone |
| `json:"x,omitzero"` for zero-value structs | `omitempty` misused on structs |
| `min` / `max` builtins | manual comparisons |

`encoding/json/v2` is still experimental - do not adopt.

## Architecture (non-negotiable boundaries)

`cmd/server` (wiring only) -> `internal/handler` (HTTP only) -> `internal/service` (no HTTP types) -> `internal/store`. Plus `internal/middleware`, `internal/config`, `internal/domain`.

- **Wiring**: plain constructor injection - `NewService(store domain.VectorStore, logger *slog.Logger)`. All graph assembly happens in `main.go` and nowhere else. No globals, no `init()` side effects, no service locators.
- **Interfaces are defined by the consumer**, sized 1-3 methods, and exist only where a boundary must be swapped (store behind `domain.VectorStore` for tests). Everything else is a concrete struct. An interface whose test double is not actually exercised by a test is dead weight - delete it. If a consumer genuinely needs more than 3 methods, split by use case, not by implementation convenience.
- **`internal/service` must compile without importing `net/http`**. If a service function needs a status code, the design is wrong.
- **Functional options** (`WithTimeout(d)`) only for constructors with 3+ *optional* parameters (required params never count); otherwise a config struct or plain params.
- **Generics** only for genuinely type-parameterized algorithms/containers. Service code uses interfaces.
- Config from env only, validated and fail-fast in `internal/config` at startup. A missing required var crashes the process with a clear message - never a zero-value fallback.

## Code construction (how every function and package is shaped)

- **Functions do one thing.** Guard clauses and early returns; happy path at minimum indentation, never nested past 2 levels. A function with distinguishable phases (whether separated by comments, blank lines, or nothing) is two functions.
- **No boolean flag parameters** - `Process(data, true)` is unreadable at the call site; split into two named functions. More than 4 params means a params struct (for constructors, the functional-options rule below takes precedence).
- **Accept interfaces, return structs.** Callers depend on the narrow consumer interface; constructors return the concrete type so callers keep full capability.
- **Composition over inheritance-simulation.** Build behavior by combining small types. Struct embedding only when the outer type is correctly usable as the embedded type at every call site; never chains of embedded structs to fake a hierarchy.
- **Make the zero value useful** (`var b strings.Builder` works; aim for the same in your types). A struct that is invalid until three setters run is a constructor that should validate instead.

### Extensibility - the sanctioned patterns

Dynamic behavior comes from *interfaces selected at wiring time*, never from reflection or `any`. The open-closed test: adding a variant must mean adding a new file/implementation, not editing existing switch arms.

- **Strategy**: one consumer interface, N implementations, chosen in `main.go` from config. House example: the check-worthiness gate - `buildPrechecker` wires the classifier-plus-coverage gate when enabled and the allow-all prechecker when disabled, behind one `SegmentPrechecker`; the pipeline cannot tell which is live.
- **Adapter**: every third-party API (Voyage, AssemblyAI) is wrapped behind a domain interface at the boundary; their wire types never leak past the adapter package. Swapping a vendor touches one package.
- **Decorator**: middleware (`func(http.Handler) http.Handler`) and store wrappers (logging/metrics around `domain.VectorStore`) layer behavior without touching the wrapped code.
- **Registry/data-driven dispatch**: when variants share a shape, a `map[string]Handler` populated at wiring beats a switch ladder - new variant, new entry, zero edits elsewhere.
- **Banned for "flexibility"**: `reflect`, `any`-typed parameters, `map[string]any` as an internal data model (it is allowed only at the jsonb boundary), runtime type switches as routing. If you need that, the design is missing an interface.
- No `util`, `common`, or `helpers` packages - a function with no domain home is in the wrong design. Name packages by what they provide (`embedding`, `transcript`), never by what they contain (`models`, `types`).

## HTTP

- `http.NewServeMux()` with method+pattern routes: `mux.HandleFunc("GET /api/items/{id}", h)`, `r.PathValue("id")`, root anchored `"GET /{$}"`. `http.DefaultServeMux` is banned.
- `http.Server` MUST set `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, AND `ReadHeaderTimeout`. Defaults are unlimited.
- Middleware is `func(http.Handler) http.Handler`. Composition order is explicit in `main.go`.
- Every handler: decode -> validate -> call service -> encode. `http.Error(...)` then `return` - the missing `return` is the classic superfluous-WriteHeader bug.
- Long handlers respect `r.Context().Done()`; everything I/O takes `ctx context.Context` as the first parameter. Context never lives in a struct field.

## Errors

- Wrap at every hop that adds information: `fmt.Errorf("loading document %s: %w", id, err)`. "Added context" means the message names the operation or entity that failed - a bare function name restated is noise; then just `return err`.
- Sentinels as `var ErrNotFound = errors.New(...)` in the owning package; match with `errors.Is` / `errors.AsType[T]`.
- An error is handled exactly once: either logged or returned, never both.
- `panic` is banned outside `main` wiring failures. Library-style code returns errors.

## Logging

`slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))` set once in `main`. Always the `Context` variants (`slog.InfoContext`) with typed attrs (`slog.String`, `slog.Int`) - untyped key-value pairs allocate and dodge vet checks. Guard expensive attrs with `logger.Enabled(ctx, level)`.

## Performance (think about it on every line)

Write the allocation-free version when it costs nothing in clarity; measure before anything clever beyond that.

- Preallocate: `make([]T, 0, n)` whenever the size is known or boundable. `prealloc` lint enforces it.
- `strings.Builder` for any concatenation in a loop; `fmt.Sprintf` is for formatting, not joining.
- Pass small structs by value; return values, not pointers, unless the struct is large or identity matters - pointers force heap escapes. Check suspicions with `go build -gcflags=-m`.
- `sync.Pool` only when a benchmark or pprof allocation profile shows the churn (escape-analysis output alone is not a measurement), never speculatively.
- Hot paths get a benchmark using `for b.Loop()` (1.24+; do not write `b.N` loops).
- Deploy: set `GOMEMLIMIT` to ~85% of the container memory limit. PGO is the sanctioned next step once prod profiles exist (`default.pgo` next to `main.go` - free 2-14%).
- `pgxpool`: set `MaxConns` (~2x cores), `MinConns` to pre-warm, `HealthCheckPeriod`. Never ship the zero-config pool.

## Data store (Postgres + pgvector, via sqlc)

Schema: `documents(id, content, metadata jsonb, embedding halfvec(1024))`, HNSW `halfvec_cosine_ops` (m=16, ef_construction=200), query with `<=>`. voyage-4-large vectors are not pre-normalized - the cosine opclass handles it, no manual normalization. Versions (verified 2026-06): pgx/v5 v5.10.0, pgvector-go v0.4.0 (+ separate `.../pgvector-go/pgx` module), pgvector ext 0.8.0 on RDS eu-west-3, sqlc 1.31.1.

All SQL goes through sqlc - hand-written query strings and ORMs are both banned. Store wraps generated `db.Queries` behind `domain.VectorStore`.

- `sqlc.yaml` + `queries/*.sql` at backend root; schema from `migrations/*.up.sql` (glob excludes down files). Generated code in `internal/store/db`, checked in, never edited.
- Regenerate via `make sqlc`; `make sqlc-verify` (`sqlc diff`) runs in CI - drift fails the PR.
- Dynamic predicates stay inside sqlc: optional filters via `sqlc.narg` + `COALESCE`/boolean-OR patterns, or a second named query. Building SQL with `strings.Builder`/`Sprintf` is the same banned hand-written query in disguise.
- pgvector gotchas (hard-won, keep): `halfvec` needs a per-**column** override to `pgvector.HalfVector` (not `db_type`) - every new vector column gets one. Use `sqlc.arg(query_embedding)` in both SELECT and ORDER BY so sqlc emits one param and HNSW drives the order (sqlc #3496). Cast distance `(... <=> ...)::float8` to get `float64`, not `interface{}`. Nil `map[string]any` encodes as SQL NULL - coerce to `{}` before insert (`metadata` is NOT NULL). Batch writes use `:batchexec` (pgx `SendBatch`). Register vector types and `SET hnsw.ef_search` in `cfg.AfterConnect`.
- Migrations: `migrations/000N_*.{up,down}.sql`, applied by golang-migrate (`make migrate-up`; compose runs a one-shot migrate service; prod applies on deploy).

## Transcription (AssemblyAI Universal-3 Pro streaming, via internal/transcribe)

AssemblyAI Universal-3 Pro streaming is the SINGLE speech-to-text provider, for both live
streams and imported videos (uploads, YouTube, bundled samples): every source streams its
playback audio over the realtime WebSocket and is transcribed and diarized live - there is no
batch transcript wait. There is NO batch transcriber and NO provider toggle. ElevenLabs Scribe
was removed entirely (its realtime path cannot diarize, and a fact-check must never blend two
speakers into one verdict). No official Go SDK exists; the direct `coder/websocket` adapter in
`internal/transcribe` is the sanctioned integration (pinned v1.8.14); community SDKs are banned.
Verified 2026-06 against assemblyai.com/docs streaming v3:

- Endpoint `wss://streaming.assemblyai.com/v3/ws` (EU: `wss://streaming.eu.assemblyai.com/v3/ws`).
  Query params: `speech_model=u3-rt-pro`, `sample_rate=16000`, `encoding=pcm_s16le`,
  `speaker_labels=true`, optional `max_speakers`. Auth is the raw API key in the `Authorization`
  header (NOT `Bearer`, NOT `xi-api-key`). The docs also list `encoding=linear16` and a `?token=`
  temp-token flow as alternatives; the server accepts the header + `pcm_s16le` form the client
  uses, so do NOT change a working dial without re-verifying against a live socket.
- Audio is sent as raw BINARY WebSocket frames of PCM s16le bytes - NEVER base64-in-JSON. Server
  messages are JSON text frames: `Begin` (session start), `Turn` (`end_of_turn=false` partial,
  `end_of_turn=true` committed), `Termination`. A `Turn` carries `transcript`, `speaker_label`,
  and `words[]` with INTEGER MILLISECOND `start`/`end`, `word_is_final`, and per-word `speaker`;
  convert ms to `time.Duration` (`*time.Millisecond`). Fatal errors arrive as a non-1000
  WebSocket close with a reason string, not a data-channel message; send `{"type":"Terminate"}`
  to end cleanly.
- The inbound read limit MUST be raised above coder/websocket's 32 KiB default (`SetReadLimit`,
  4 MiB): a formatted `Turn` for a long utterance carries a per-word array that exceeds it, and
  the default turns that into a fatal read error that kills the live session.
- The `transcribe` package exposes ONLY the streaming contract (the `streamClient` consumer
  interface: `TranscribeStream(ctx, chunks, opts) (<-chan TranscriptEvent, error)`); there is no
  `TranscribeFile` or two-method `Transcriber` interface. `StreamSegmenter` adapts it to the
  service layer's `SegmentStream` port.
- Key/model/speakers from `config.LoadTranscription` (`TRANSCRIPTION_API_KEY` required,
  `TRANSCRIPTION_MODEL` default `u3-rt-pro`, optional `TRANSCRIPTION_MAX_SPEAKERS`); fail fast,
  never log the key. Exactly ONE transcription secret exists across `.env`, docker-compose, and
  Terraform Secrets Manager.

## Testing

- Table-driven with `t.Run(tc.name, ...)`; `t.Parallel()` wherever tests are independent; `t.Context()` instead of `context.Background()`.
- Handlers via `httptest.NewRecorder` against a `newMux()`-style constructor; integration via `httptest.NewServer`.
- **Stdlib assertions + `go-cmp` for deep diffs. Do not add testify** - it is not imported today and stays that way.
- Concurrency/timer logic: `testing/synctest` (stable 1.25) - deterministic fake clock, no `time.Sleep` in tests, ever.
- Fuzz parsers and validation (`go test -fuzz`); store integration tests skip without `TEST_DATABASE_URL` (compose provides `pgvector/pgvector:pg16`).
- CI: `go test -race ./...`. A change without tests in the same diff does not merge.

## Lint

golangci-lint **v2** config (`version: "2"`, `linters.default: standard`) plus: `gocritic`, `noctx`, `prealloc`, `unparam`, `revive`, `bodyclose`, `misspell`, `godot`. `wrapcheck` is intentionally NOT enabled (too noisy at boundaries); do not add it, or any `nolint`, without a written decision in the PR description. `gofumpt` (not just gofmt) + `go vet ./...` locally before every commit.

## Red flags - stop and fix

- "A framework/router/ORM would be faster here" - banned; the constraint is the design.
- "I'll define the interface next to the implementation" - consumer side, or not at all.
- An exported function without a doc comment; a `context.Background()` outside `main`/tests.
- `time.Sleep` in a test; `b.N` in a new benchmark; `errors.As` in new code.
- "Optimize later" used to justify a known O(n^2) or per-request allocation that a one-line prealloc removes now.
