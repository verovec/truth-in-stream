# Local dev stack and layered backend skeleton (VER-13) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring up a local docker-compose stack (Next.js frontend + Go `net/http` backend + embedded Ladybug graph DB) with a clean dependency-inverted backend skeleton and a `/healthz` endpoint that proves the API reaches the database.

**Architecture:** Domain-centric layering. `handler -> service -> domain <- store/graph`. `domain` holds the repository interface (port); `store/graph` is the only package importing `lbug` (cgo); `cmd/server` wires concretes. The Ladybug single-`READ_WRITE`-handle constraint is contained behind the `domain.GraphRepository` interface so the engine can later be extracted without touching `service`/`handler`.

**Tech Stack:** Go 1.26 (`go.mod` `go 1.26`, as scaffolded; build/run in-container), `github.com/LadybugDB/go-ladybug v0.17.0` (cgo, engine v0.17.1), stdlib `net/http` + `log/slog`, docker-compose, multi-stage Dockerfile. Spec: `docs/superpowers/specs/2026-06-09-local-dev-stack-ladybug-design.md`.

---

## File map

Created under `stack/backend/`:

| File | Responsibility |
|---|---|
| `go.mod`, `go.sum` | module `github.com/verovec/truth-in-stream/backend`, `go 1.26` (scaffolded) |
| `internal/config/config.go` (+ `_test.go`) | env -> typed `Config`, fail-fast |
| `internal/domain/graph.go` | `GraphRepository` port (the seam) |
| `internal/httpx/respond.go` (+ `_test.go`) | JSON + error response helpers |
| `internal/middleware/middleware.go` (+ `_test.go`) | request-id, recovery, logging |
| `internal/service/health.go` (+ `_test.go`) | `HealthChecker` over `GraphRepository` |
| `internal/handler/handler.go`, `internal/handler/health.go` (+ `_test.go`) | `NewMux` router + health controller |
| `internal/store/graph/graph.go`, `generate.go` (+ `smoke_test.go`) | Ladybug repository (cgo) |
| `cmd/server/main.go` | wiring + graceful shutdown |
| `Dockerfile` | multi-stage `dev` / `builder` / `prod` |
| `.dockerignore` | build context trim |
| `.gitignore` (append) | ignore `lib-ladybug/`, local `*.db` data |

Modified at repo root:

| File | Change |
|---|---|
| `docker-compose.yml` | backend builds from `Dockerfile` `dev` target; add `LADYBUG_PATH` + `ladybug-data` volume; drop the stock `golang:1.26` inline image |

Conventions: package code never imports a layer "above" it. Only `store/graph` and `cmd/server` may import `lbug`. Test commands assume CWD `stack/backend`.

---

## Task 0: Local Go toolchain

**Files:** none (environment only).

- [ ] **Step 1: Check local Go version**

Run: `go version`
If it reports `go1.22` or newer, skip to Task 1. The local machine currently has `go1.20`, which lacks `log/slog` and `ServeMux` pattern routing used throughout this plan.

- [ ] **Step 2: Install Go 1.26 locally (for fast local unit tests)**

Run:
```bash
go install golang.org/dl/go1.26.4@latest
go1.26.4 download
go1.26.4 version
```
Expected: `go version go1.26.4 ...`. Use `go1.26.4` in place of `go` for local commands in this plan, or update the system Go. Cgo/Ladybug tests run in-container regardless, so this only speeds up the pure-Go unit loop.

---

## Task 1: Verify module (already scaffolded)

The `/setup` scaffold already created `stack/backend/go.mod` with the correct module path
and `go 1.26`. Verify only; do not reinitialize, do not commit (nothing changes).

**Files:** none.

- [ ] **Step 1: Verify go.mod**

Run: `cat stack/backend/go.mod`
Expected: `module github.com/verovec/truth-in-stream/backend` and `go 1.26`.
If it differs, `cd stack/backend && go1.26.4 mod edit -module github.com/verovec/truth-in-stream/backend` and ensure `go 1.26`.

No commit for this task.

---

## Task 2: Spike — prove go-ladybug builds and queries in the container

De-risk the one uncertain integration before building on it. This produces a **throwaway** program and the exact cgo flags that Task 8 and the Dockerfile will reuse. `go-ladybug` is pre-v1; if the API or flags differ from below, record the working version here and propagate.

**Files:**
- Create (throwaway): `stack/backend/cmd/spike/main.go`

- [ ] **Step 1: Write the spike program**

```go
package main

import (
	"fmt"
	"os"

	lbug "github.com/LadybugDB/go-ladybug"
)

func main() {
	db, err := lbug.OpenDatabase(os.Args[1], lbug.DefaultSystemConfig())
	if err != nil {
		panic(err)
	}
	defer db.Close()

	conn, err := lbug.OpenConnection(db)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	res, err := conn.Query("RETURN 1 AS ok")
	if err != nil {
		panic(err)
	}
	defer res.Close()

	if res.HasNext() {
		tuple, _ := res.Next()
		defer tuple.Close()
		fmt.Println("SPIKE OK:", tuple.GetAsSlice())
		return
	}
	panic("no rows")
}
```

- [ ] **Step 2: Add the dependency and the native-lib fetch, then build+run in the container**

Run from `stack/backend`:
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.26 bash -c '
  set -e
  apt-get update && apt-get install -y gcc libstdc++-dev curl ca-certificates
  go get github.com/LadybugDB/go-ladybug@v0.17.0
  curl -fsSL https://raw.githubusercontent.com/LadybugDB/ladybug/refs/heads/main/scripts/download-liblbug.sh \
    | LBUG_TARGET_DIR=/app/lib-ladybug bash
  export CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"
  CGO_ENABLED=1 go run ./cmd/spike /tmp/spikedb
'
```
Expected: prints `SPIKE OK: [1]` (or similar). If `OpenDatabase`/`OpenConnection`/`Query` names differ, fix the spike against `https://pkg.go.dev/github.com/LadybugDB/go-ladybug` and **record the corrected API + flags as a comment block at the top of this task** before continuing.

- [ ] **Step 3: Record the working incantation**

Confirm and note for reuse: the build needs `gcc` + `libstdc++-dev`, the `download-liblbug.sh` fetch into `lib-ladybug/`, and `CGO_LDFLAGS="-L<dir> -llbug -Wl,-rpath,<dir>"`. These are the canonical values for Task 8 and the Dockerfile.

- [ ] **Step 4: Remove the throwaway and ignore the native lib**

Run:
```bash
rm -rf stack/backend/cmd/spike
printf '\n# native graph lib (fetched via go generate)\nlib-ladybug/\n*.db\n' >> stack/backend/.gitignore
```
(Create `stack/backend/.gitignore` if absent.)

- [ ] **Step 5: Commit**

```bash
git add stack/backend/go.mod stack/backend/go.sum stack/backend/.gitignore
git commit -m "chore(backend): add go-ladybug dependency, ignore native lib"
```

---

## Task 3: Config package

**Files:**
- Create: `stack/backend/internal/config/config.go`
- Test: `stack/backend/internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

```go
package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults applied",
			env:  map[string]string{"LADYBUG_PATH": "/data/ladybug"},
			want: Config{Port: "8080", LadybugPath: "/data/ladybug"},
		},
		{
			name:    "missing ladybug path fails",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "port override",
			env:  map[string]string{"PORT": "9090", "LADYBUG_PATH": "/data/ladybug"},
			want: Config{Port: "9090", LadybugPath: "/data/ladybug"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go1.26.4 test ./internal/config/ -v`
Expected: FAIL (undefined: `Load`, `Config`).

- [ ] **Step 3: Write minimal implementation**

```go
// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
)

// Config holds the runtime configuration for the server.
type Config struct {
	Port        string
	LadybugPath string
}

// Load reads configuration from the environment, applying defaults and
// failing fast when a required variable is missing.
func Load() (Config, error) {
	cfg := Config{
		Port:        getenv("PORT", "8080"),
		LadybugPath: os.Getenv("LADYBUG_PATH"),
	}
	if cfg.LadybugPath == "" {
		return Config{}, fmt.Errorf("config: LADYBUG_PATH is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go1.26.4 test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/config/
git commit -m "feat(backend): add config loader"
```

---

## Task 4: Domain port

**Files:**
- Create: `stack/backend/internal/domain/graph.go`

No test: this file declares an interface only (no behavior to test). Models for claims/sources arrive with their feature cards (VER-6+).

- [ ] **Step 1: Write the interface**

```go
// Package domain holds core models and repository interfaces (ports).
// It imports nothing internal; all layers depend inward on this package.
package domain

import "context"

// GraphRepository is the port for the graph datastore. The concrete
// implementation (store/graph) is the only place that knows about the
// embedded engine, so the engine can be swapped or extracted behind this
// interface without changing service or handler code.
type GraphRepository interface {
	// Ping verifies the graph datastore is reachable.
	Ping(ctx context.Context) error
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go1.26.4 build ./internal/domain/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add stack/backend/internal/domain/
git commit -m "feat(backend): add domain graph repository port"
```

---

## Task 5: HTTP response helpers (httpx)

**Files:**
- Create: `stack/backend/internal/httpx/respond.go`
- Test: `stack/backend/internal/httpx/respond_test.go`

- [ ] **Step 1: Write the failing test**

```go
package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusOK, map[string]string{"status": "ok"})

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v, want status=ok", body)
	}
}

func TestError(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusServiceUnavailable, "graph unavailable")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if body["error"] != "graph unavailable" {
		t.Fatalf("body = %v, want error message", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go1.26.4 test ./internal/httpx/ -v`
Expected: FAIL (undefined: `JSON`, `Error`).

- [ ] **Step 3: Write minimal implementation**

```go
// Package httpx holds shared HTTP encoding helpers used by handlers.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpx: encode response", slog.Any("err", err))
	}
}

// Error writes a JSON error body with the given status code.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go1.26.4 test ./internal/httpx/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/httpx/
git commit -m "feat(backend): add httpx response helpers"
```

---

## Task 6: Middleware

**Files:**
- Create: `stack/backend/internal/middleware/middleware.go`
- Test: `stack/backend/internal/middleware/middleware_test.go`

- [ ] **Step 1: Write the failing test**

```go
package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestIDSetsHeader(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("expected X-Request-Id header to be set")
	}
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil)) // must not panic

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go1.26.4 test ./internal/middleware/ -v`
Expected: FAIL (undefined: `RequestID`, `Recover`).

- [ ] **Step 3: Write minimal implementation**

```go
// Package middleware holds net/http middleware wrappers.
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// RequestID assigns each request an X-Request-Id response header if absent.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = newID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

// Recover converts a panic in a downstream handler into a 500 response.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered", slog.Any("panic", rec), slog.String("path", r.URL.Path))
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Logging logs one structured line per request with method, path, and duration.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)
			logger.InfoContext(r.Context(), "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go1.26.4 test ./internal/middleware/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/middleware/
git commit -m "feat(backend): add request-id, recover, logging middleware"
```

---

## Task 7: Health service

**Files:**
- Create: `stack/backend/internal/service/health.go`
- Test: `stack/backend/internal/service/health_test.go`

- [ ] **Step 1: Write the failing test**

```go
package service

import (
	"context"
	"errors"
	"testing"
)

type fakeGraph struct{ err error }

func (f fakeGraph) Ping(ctx context.Context) error { return f.err }

func TestHealthCheckerCheck(t *testing.T) {
	tests := []struct {
		name    string
		repoErr error
		wantErr bool
	}{
		{name: "graph healthy", repoErr: nil, wantErr: false},
		{name: "graph down", repoErr: errors.New("dial fail"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hc := NewHealthChecker(fakeGraph{err: tc.repoErr})
			err := hc.Check(context.Background())
			if tc.wantErr != (err != nil) {
				t.Fatalf("Check() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go1.26.4 test ./internal/service/ -v`
Expected: FAIL (undefined: `NewHealthChecker`).

- [ ] **Step 3: Write minimal implementation**

```go
// Package service holds business logic. It depends only on domain interfaces.
package service

import (
	"context"
	"fmt"

	"github.com/verovec/truth-in-stream/backend/internal/domain"
)

// HealthChecker reports whether dependencies are reachable.
type HealthChecker struct {
	graph domain.GraphRepository
}

// NewHealthChecker builds a HealthChecker over the given graph repository.
func NewHealthChecker(graph domain.GraphRepository) *HealthChecker {
	return &HealthChecker{graph: graph}
}

// Check returns nil when all dependencies are healthy.
func (h *HealthChecker) Check(ctx context.Context) error {
	if err := h.graph.Ping(ctx); err != nil {
		return fmt.Errorf("graph: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go1.26.4 test ./internal/service/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/service/
git commit -m "feat(backend): add health service over graph port"
```

---

## Task 8: HTTP handler and router

**Files:**
- Create: `stack/backend/internal/handler/handler.go`
- Create: `stack/backend/internal/handler/health.go`
- Test: `stack/backend/internal/handler/handler_test.go`

- [ ] **Step 1: Write the failing test**

```go
package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/verovec/truth-in-stream/backend/internal/service"
)

type fakeGraph struct{ err error }

func (f fakeGraph) Ping(ctx context.Context) error { return f.err }

func newTestServer(graphErr error) http.Handler {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	hc := service.NewHealthChecker(fakeGraph{err: graphErr})
	return NewMux(hc, logger)
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name     string
		graphErr error
		wantCode int
	}{
		{name: "healthy", graphErr: nil, wantCode: http.StatusOK},
		{name: "graph down", graphErr: errors.New("down"), wantCode: http.StatusServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.graphErr)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rec.Code != tc.wantCode {
				t.Fatalf("GET /healthz = %d, want %d", rec.Code, tc.wantCode)
			}
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go1.26.4 test ./internal/handler/ -v`
Expected: FAIL (undefined: `NewMux`).

- [ ] **Step 3: Write the router**

`internal/handler/handler.go`:
```go
// Package handler holds HTTP controllers and the router constructor.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/middleware"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// NewMux builds the application router with middleware applied.
func NewMux(health *service.HealthChecker, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler(health))

	var h http.Handler = mux
	h = middleware.Logging(logger)(h)
	h = middleware.Recover(logger)(h)
	h = middleware.RequestID(h)
	return h
}
```

- [ ] **Step 4: Write the health controller**

`internal/handler/health.go`:
```go
package handler

import (
	"net/http"

	"github.com/verovec/truth-in-stream/backend/internal/httpx"
	"github.com/verovec/truth-in-stream/backend/internal/service"
)

// healthHandler returns 200 when dependencies are healthy, else 503.
func healthHandler(health *service.HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := health.Check(r.Context()); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "unhealthy")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go1.26.4 test ./internal/handler/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add stack/backend/internal/handler/
git commit -m "feat(backend): add router and healthz controller"
```

---

## Task 9: Ladybug store (cgo)

This is the only package importing `lbug`. Build/test run **in the container** because of the cgo native lib. Uses the API and flags confirmed in Task 2.

**Files:**
- Create: `stack/backend/internal/store/graph/graph.go`
- Create: `stack/backend/internal/store/graph/generate.go`
- Test: `stack/backend/internal/store/graph/smoke_test.go`

- [ ] **Step 1: Write the go:generate directive**

`internal/store/graph/generate.go`:
```go
package graph

// Fetches the prebuilt Ladybug native library into <module-root>/lib-ladybug
// before build. Run `go generate ./...` with network access. The target dir is
// resolved from `go env GOMOD` so it works from any CWD and without a .git dir
// (the build containers mount only stack/backend).
//go:generate sh -c "curl -fsSL https://raw.githubusercontent.com/LadybugDB/ladybug/refs/heads/main/scripts/download-liblbug.sh | LBUG_TARGET_DIR=\"$(dirname \"$(go env GOMOD)\")/lib-ladybug\" bash"
```

- [ ] **Step 2: Write the repository**

`internal/store/graph/graph.go`:
```go
// Package graph implements domain.GraphRepository on the embedded Ladybug engine.
// It owns the single process-wide READ_WRITE database handle.
package graph

import (
	"context"
	"fmt"

	lbug "github.com/LadybugDB/go-ladybug"
)

// Repository wraps the single Ladybug database handle for the process.
type Repository struct {
	db *lbug.Database
}

// Open opens (or creates) the Ladybug database at path.
func Open(path string) (*Repository, error) {
	db, err := lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("graph: open %q: %w", path, err)
	}
	return &Repository{db: db}, nil
}

// Ping verifies the engine answers a trivial query.
func (r *Repository) Ping(ctx context.Context) error {
	conn, err := lbug.OpenConnection(r.db)
	if err != nil {
		return fmt.Errorf("graph: open connection: %w", err)
	}
	defer conn.Close()

	res, err := conn.Query("RETURN 1")
	if err != nil {
		return fmt.Errorf("graph: ping query: %w", err)
	}
	res.Close()
	return nil
}

// Close releases the database handle.
func (r *Repository) Close() error {
	r.db.Close()
	return nil
}
```
If Task 2 recorded a different `lbug` API, apply the same corrections here.

- [ ] **Step 3: Write the smoke test**

`internal/store/graph/smoke_test.go`:
```go
package graph

import (
	"context"
	"testing"
)

func TestOpenPingClose(t *testing.T) {
	repo, err := Open(t.TempDir() + "/smoke.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer repo.Close()

	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
```

- [ ] **Step 4: Run the store test in the container**

Run from `stack/backend`:
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.26 bash -c '
  set -e
  apt-get update && apt-get install -y gcc libstdc++-14-dev curl ca-certificates
  go generate ./...
  export CGO_CFLAGS="-I/app/lib-ladybug"
  export CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"
  CGO_ENABLED=1 go test -tags system_ladybug ./internal/store/graph/ -v
'
```
Expected: PASS (`TestOpenPingClose`).
Note (confirmed by the Task 2 spike): the `-tags system_ladybug` build tag, the `CGO_CFLAGS`
header include, and the `libstdc++-14-dev` package are all required; omitting any of them
breaks the build.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/internal/store/graph/ stack/backend/go.mod stack/backend/go.sum
git commit -m "feat(backend): add ladybug graph repository"
```

---

## Task 10: Server wiring and graceful shutdown

The scaffold left a self-contained `cmd/server/main.go` (with its own `newMux`/`handleHealthz`)
and `cmd/server/main_test.go` (tests `newMux()`). Health now lives in `internal/handler`,
so this task **replaces** `main.go` with the wiring below and **deletes** the obsolete
`cmd/server/main_test.go` (its behavior is covered by `internal/handler/handler_test.go`).

**Files:**
- Replace: `stack/backend/cmd/server/main.go`
- Delete: `stack/backend/cmd/server/main_test.go`

- [ ] **Step 1: Delete the obsolete package-main test**

Run: `rm stack/backend/cmd/server/main_test.go`

- [ ] **Step 2: Write main**

```go
// Command server is the truth-in-stream backend API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/verovec/truth-in-stream/backend/internal/config"
	"github.com/verovec/truth-in-stream/backend/internal/handler"
	"github.com/verovec/truth-in-stream/backend/internal/service"
	"github.com/verovec/truth-in-stream/backend/internal/store/graph"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server exited with error", slog.Any("err", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	graphRepo, err := graph.Open(cfg.LadybugPath)
	if err != nil {
		return err
	}
	defer graphRepo.Close()

	health := service.NewHealthChecker(graphRepo)
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler.NewMux(health, logger),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
```

- [ ] **Step 3: Compile the full module in the container**

Run from `stack/backend`:
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.26 bash -c '
  set -e
  apt-get update && apt-get install -y gcc libstdc++-14-dev curl ca-certificates
  go generate ./...
  export CGO_CFLAGS="-I/app/lib-ladybug"
  export CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"
  CGO_ENABLED=1 go build -tags system_ladybug ./...
'
```
Expected: success (no output).

- [ ] **Step 4: Commit**

```bash
git add stack/backend/cmd/server/
git commit -m "feat(backend): wire server with graceful shutdown"
```

---

## Task 11: Backend Dockerfile (multi-stage)

**Files:**
- Modify/replace: `stack/backend/Dockerfile`
- Create: `stack/backend/.dockerignore`

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# syntax=docker/dockerfile:1

# Shared base with the C toolchain and native Ladybug lib (flags confirmed in Task 2).
FROM golang:1.26 AS base
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc libstdc++-14-dev curl ca-certificates && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go generate ./...
ENV CGO_ENABLED=1
ENV CGO_CFLAGS="-I/app/lib-ladybug"
ENV CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"

# Dev: run with `go run` (source bind-mounted by compose overrides /app).
# The bundled-vs-system tag must be set, per Task 2.
FROM base AS dev
EXPOSE 8080
CMD ["go", "run", "-tags", "system_ladybug", "./cmd/server"]

# Builder: shared-lib link (the validated path). Reuses base so the native lib and
# flags are identical. The binary's rpath is /app/lib-ladybug.
FROM base AS builder
RUN go build -tags system_ladybug -o /out/server ./cmd/server

# Prod: distroless/cc (ships libstdc++/libgcc + glibc loader for cgo), non-root.
# Recreate /app/lib-ladybug so the binary's baked rpath resolves liblbug.so.0 at load.
FROM gcr.io/distroless/cc-debian12:nonroot AS prod
COPY --from=builder /app/lib-ladybug /app/lib-ladybug
COPY --from=builder /out/server /server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
```
Notes: `-tags system_ladybug` + `CGO_CFLAGS`/`CGO_LDFLAGS` + `libstdc++-14-dev` are the
exact recipe the Task 2 spike validated. We ship the shared `liblbug.so` into the runtime
(via the baked rpath) rather than static-linking, because the shared path is the one that
was actually verified. `distroless/cc` (not `distroless/static`, which the scaffold used)
carries `libstdc++`/`libgcc`.

- [ ] **Step 2: Write .dockerignore**

```
.git
lib-ladybug
*.db
**/*_test.go
```

- [ ] **Step 3: Build the dev and prod targets**

Run from `stack/backend`:
```bash
docker build --target dev  -t tis-backend:dev  .
docker build --target prod -t tis-backend:prod .
```
Expected: both builds succeed.

- [ ] **Step 4: Smoke-run the prod image (expect config failure, not a loader crash)**

Run: `docker run --rm tis-backend:prod`
Expected: exits non-zero with a JSON log line containing `LADYBUG_PATH is required`. This
proves two things: the dynamic loader resolved `liblbug.so` via the baked rpath (otherwise
you'd see `error while loading shared libraries: liblbug.so.0`), and config fails fast.

- [ ] **Step 5: Commit**

```bash
git add stack/backend/Dockerfile stack/backend/.dockerignore
git commit -m "build(backend): multi-stage dev/prod Dockerfile for ladybug cgo"
```

---

## Task 12: Wire docker-compose

**Files:**
- Modify: `docker-compose.yml`

- [ ] **Step 1: Replace the backend service and add the ladybug volume**

Full file content:
```yaml
# Local development. Source is bind-mounted and dev servers run with hot reload.
# Production images are built from stack/*/Dockerfile (used by the deploy workflow).
services:
  backend:
    build:
      context: ./stack/backend
      target: dev
    working_dir: /app
    environment:
      PORT: "8080"
      LADYBUG_PATH: "/data/ladybug"
    ports:
      - "8080:8080"
    volumes:
      - ./stack/backend:/app
      - go-mod-cache:/go/pkg/mod
      - ladybug-data:/data   # mount the PARENT; Ladybug creates /data/ladybug itself

  frontend:
    image: node:22-alpine
    working_dir: /app
    command: sh -c "npm install && npm run dev"
    environment:
      NEXT_TELEMETRY_DISABLED: "1"
      NEXT_PUBLIC_API_URL: "http://localhost:8080"
    ports:
      - "3000:3000"
    volumes:
      - ./stack/frontend:/app
      - frontend-node-modules:/app/node_modules
    depends_on:
      - backend

volumes:
  go-mod-cache:
  frontend-node-modules:
  ladybug-data:
```
Note: the dev stage runs `go generate` and sets `CGO_LDFLAGS` in the image; the bind mount overlays source but `lib-ladybug/` is produced by `go generate` during build into the image layer. Because the bind mount shadows `/app`, run `go generate` on the host once (Step 2) so `lib-ladybug/` exists in the mounted tree.

- [ ] **Step 2: Populate lib-ladybug in the mounted source tree**

Run from `stack/backend`:
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.26 bash -c '
  apt-get update && apt-get install -y curl ca-certificates && go generate ./...'
```
Expected: `lib-ladybug/liblbug.so` exists on the host (gitignored).

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml
git commit -m "build: run backend from dev Dockerfile with ladybug volume"
```

---

## Task 13: End-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Bring up the stack**

Run from repo root: `docker compose up --build -d backend`
Expected: `backend` container starts and logs `listening addr=:8080`.

- [ ] **Step 2: Hit the health endpoint**

Run: `curl -fsS -w '\n%{http_code}\n' http://localhost:8080/healthz`
Expected: body `{"status":"ok"}` and HTTP `200`.

- [ ] **Step 3: Verify persistence across restart**

Run:
```bash
docker compose restart backend
sleep 3
curl -fsS -w '\n%{http_code}\n' http://localhost:8080/healthz
```
Expected: `200` again; the `ladybug-data` volume retained the database directory (no re-init error in logs).

- [ ] **Step 4: Bring up the full stack**

Run: `docker compose up --build -d`
Expected: `frontend` reachable at `http://localhost:3000`, `backend` at `http://localhost:8080/healthz`.

- [ ] **Step 5: Tear down**

Run: `docker compose down`

- [ ] **Step 6: Commit any compose fixes discovered**

```bash
git add -A
git commit -m "test: verify local stack health and persistence" || echo "nothing to commit"
```

---

## Task 14: Lint pass

**Files:** none (may touch source for fixes).

- [ ] **Step 1: Run gofmt, vet, and golangci-lint in the container**

Run from `stack/backend`. Use a Debian (glibc) container, not the Alpine golangci-lint
image — the native lib is glibc and won't load under musl. gofmt + vet are hard gates;
golangci-lint is best-effort here (CI is the source of truth):
```bash
docker run --rm -v "$PWD":/app -w /app golang:1.26 bash -c '
  set -e
  apt-get update && apt-get install -y gcc libstdc++-14-dev curl ca-certificates
  go generate ./...
  export CGO_CFLAGS="-I/app/lib-ladybug"
  export CGO_LDFLAGS="-L/app/lib-ladybug -llbug -Wl,-rpath,/app/lib-ladybug"
  test -z "$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
  go vet -tags system_ladybug ./...
  curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b /usr/local/bin
  golangci-lint run --build-tags system_ladybug ./... || echo "golangci-lint findings above (review)"
'
```
Expected: no gofmt diffs, no vet errors. Review any golangci-lint findings.

- [ ] **Step 2: Fix any findings and re-run until clean, then commit**

```bash
git add -A
git commit -m "style(backend): satisfy gofmt/vet/golangci-lint" || echo "nothing to commit"
```

---

## Done criteria (maps to VER-13 acceptance)

- `docker compose up` starts frontend + backend + embedded Ladybug — Tasks 12, 13.
- Frontend on 3000, backend on 8080 — Task 13.
- `/healthz` succeeds only when Ladybug responds — Tasks 7, 8, 9, 13.
- Data persists across restart via `ladybug-data` — Task 13.
- Layered backend, DB only via the `domain.GraphRepository` interface — Tasks 4, 7, 8, 9.
- Production image builds from the same source — Task 11.
