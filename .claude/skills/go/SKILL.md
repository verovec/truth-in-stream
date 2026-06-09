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
