---
name: go
description: Use when working on the Go backend in stack/backend - idiomatic stdlib net/http service, slog logging, layout, testing, and linting
---

# Go backend (stack/backend)

Module `github.com/verovec/truth-in-stream/backend`. Standard-library `net/http` service, no web framework. Entry point `cmd/server/main.go` with graceful shutdown and a `/healthz` endpoint.

## Toolchain
Latest stable is Go 1.26.x. **Target Go 1.22 minimum** (`go` directive in go.mod), build with 1.26 in CI.

The local machine currently has **Go 1.20**, which lacks features this skill assumes. Upgrade locally (`go install golang.org/dl/go1.26.4@latest && go1.26.4 download`, or update the system Go). What each version unlocks:
- 1.21: `log/slog`, `slices`/`maps`, `min`/`max`.
- 1.22: `ServeMux` method+wildcard routing (`"GET /path/{id}"`), `for i := range N`.
- 1.23: `iter` package / range-over-func.

Until upgraded, the scaffold uses the `log` package and a manual method check instead of `slog` and pattern routing.

## HTTP structure (1.22+)
- `mux := http.NewServeMux()`; register `mux.HandleFunc("GET /api/items/{id}", h)`; read wildcards with `r.PathValue("id")`; anchor root with `"GET /{$}"`. Never use `http.DefaultServeMux`.
- Middleware as `func(http.Handler) http.Handler` wrappers.
- Always set `http.Server` `ReadTimeout`/`WriteTimeout`/`IdleTimeout`.
- Graceful shutdown via `signal.NotifyContext` + `srv.Shutdown(ctx)` (already wired in main.go).

## Logging (slog, once on 1.21+)
`slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))`. Use `slog.InfoContext(ctx, "msg", slog.String(...))` with typed attrs. Don't mix `log.SetOutput` after setting an slog default.

## Layout
`cmd/server` (thin wiring only) · `internal/handler` · `internal/service` (no HTTP types) · `internal/store` · `internal/middleware` · `internal/config`. `internal/` enforces the encapsulation boundary. Config from env, fail fast on missing required vars. Wrap errors with `fmt.Errorf("...: %w", err)`; check with `errors.Is`/`errors.As`.

## Data store (Postgres + pgvector)
Vector retrieval runs on Postgres + pgvector (replaced the abandoned Ladybug/Kuzu engine). Pure ANN similarity search, not graph.
- Versions (verified 2026-06): `github.com/jackc/pgx/v5` v5.10.0, `github.com/pgvector/pgvector-go` v0.4.0 (the pgx adapter `.../pgvector-go/pgx` is a separate module — both land in go.mod). pgvector extension 0.8.0 on RDS/Aurora (eu-west-3); 0.8.2 upstream. RDS for now; Aurora is a later Terraform-only swap.
- Schema: `documents(id, content, metadata jsonb, embedding halfvec(1024))`; HNSW index `halfvec_cosine_ops` (m=16, ef_construction=200). Query with `<=>` (cosine). voyage-4 embeddings are not pre-normalized — cosine opclass handles that, no manual normalization.
- Driver: `pgxpool`; register vector types AND `SET hnsw.ef_search` in `cfg.AfterConnect` (runs per pooled connection). Embeddings go over the wire via `pgvector.NewHalfVector([]float32)`.
- Gotcha: a nil `map[string]any` encodes as SQL NULL — coerce to `map[string]any{}` before insert since `metadata` is NOT NULL.
- Migrations live in `stack/backend/migrations/000N_*.{up,down}.sql` (golang-migrate format). Terraform RDS module is a separate card.
- Local: store integration tests skip unless `TEST_DATABASE_URL` is set; `docker compose` provides a `pgvector/pgvector:pg16` service.

### SQL via sqlc (not an ORM)
Queries are written in SQL and compiled to type-safe Go by sqlc — no hand-written `pgx` query strings, no ORM. The store package wraps the generated `db.Queries` behind `domain.VectorStore`.
- Layout: `sqlc.yaml` + `queries/*.sql` at backend root; schema source is `migrations/*.up.sql` (glob excludes the down files so their DROPs don't cancel the schema); generated code lands in `internal/store/db` (package `db`, checked in, `// DO NOT EDIT`).
- Regenerate after any schema/query change, then `go mod tidy` + `go test`:
  `docker run --rm -v "$PWD":/src -w /src sqlc/sqlc:1.31.1 generate` (run from `stack/backend`).
- pgvector gotchas baked into the config: `halfvec` has no built-in sqlc mapping — use a per-**column** override (`documents.embedding` -> `pgvector.HalfVector`), not `db_type`. Reference the query vector via a named arg (`sqlc.arg(query_embedding)`) used in both SELECT and ORDER BY so sqlc collapses it to one param and the HNSW index still drives the order (avoids the repeated-`$1` mis-numbering, sqlc #3496). Cast the distance `(... <=> ...)::float8` so sqlc emits `float64` instead of `interface{}`.
- `jsonb` generates as `[]byte`; the store marshals/unmarshals `map[string]any` at the boundary. Batch writes use a `:batchexec` query (pgx `SendBatch`).
- sqlc v1.31.1 (verified 2026-06). If the query surface grows, keep using sqlc; do not introduce GORM/an ORM.
- Tasks run through `stack/backend/Makefile` (Docker-pinned tooling, no local install): `make sqlc` (regenerate), `make sqlc-verify` (drift check, `sqlc diff`), `make migrate-up`/`migrate-down`, `make test`/`test-integration`. CI runs `sqlc diff` in the lint workflow and the store integration test against a `pgvector/pgvector:pg16` service, so generated-code drift and schema breaks fail the PR.
- Migrations are applied by golang-migrate (`migrate/migrate` image). `docker compose up` runs a one-shot `migrate` service before the backend so the dev schema is ready; production applies migrations as a deploy step. Every new `halfvec`/`vector` column needs its own per-column override in `sqlc.yaml`.

## Testing
Table-driven (`t.Run(tc.name, ...)`), `net/http/httptest` (`NewRecorder` for handlers, `NewServer` for integration). Keep handler wiring testable via a `newMux()`-style constructor. Stdlib is enough; `testify` `require`/`assert` is acceptable if the team wants it. CI runs `go test -race ./...`.

## Linting
`gofmt`/`gofumpt`, `go vet ./...`, and `golangci-lint run ./...`. CI runs gofmt-check + go vet + golangci-lint-action. Good additions beyond defaults: `bodyclose`, `noctx`, `revive`.

## Pitfalls
1. Missing `http.Server` timeouts (connection leak).
2. Using `http.DefaultServeMux`.
3. `http.Error` does not return - missing `return` causes superfluous-WriteHeader.
4. Ignoring `ctx.Done()` in long handlers - stalls graceful shutdown.
5. Logging and returning the same error - pick one layer.
