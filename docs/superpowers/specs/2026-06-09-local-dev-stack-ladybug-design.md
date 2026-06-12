# Local dev stack + layered backend skeleton (Ladybug)

> SUPERSEDED (2026-06-09): This design was implemented and merged, then the backend
> store was reworked on `main` from the embedded Ladybug graph engine to Postgres +
> pgvector (sqlc), to serve the vector-search needs of the verification database
> (VER-6/VER-9). The layered architecture here (domain ports, service, handler,
> httpx, middleware) carried over unchanged; only `store/graph` + `go-ladybug` + the
> cgo Dockerfile and Ladybug compose volume were replaced by `store/postgres` +
> `store/db` + `domain.VectorStore`. Kept for history.

Date: 2026-06-09
Status: Superseded by the Postgres + pgvector implementation on `main`
Scope: Backend (`stack/backend`) and `docker-compose.yml` only
Roadmap: VER-13 (foundational infra preceding VER-6..VER-12).

## 1. Goal

Stand up the local dev runtime (docker-compose) and the clean, layered Go backend skeleton
that the v1 fact-check cards (VER-6..VER-12) will build on. The runtime runs the Next.js
frontend, the Go `net/http` backend, and an **embedded Ladybug graph database**. Establish
the layering, the database seam, and a verifiable health signal now. No domain logic, no
auth, no provisioning.

`truth-in-stream` is real-time fact-checking for live streams. The domain (claims,
entities, sources, evidence relationships) is graph-shaped, and VER-6/VER-9 need vector
similarity over claims. Ladybug fits both: a property graph with vector indices and
full-text search. It owns the domain, including the verification database.

## 2. Roadmap alignment

The Linear project "Truth in Stream" has one milestone, `v1: thin end-to-end fact-check`,
with cards VER-6..VER-12 (all Backlog). None cover infrastructure, and **none cover user
management / auth** in v1. Consequences for this task:

- **Postgres is deferred.** v1 has no auth card, so introducing a relational store for
  users now would be ahead of the roadmap. The domain store seam is built so Postgres can
  be added cleanly when an auth card exists.
- **The verification database lives in Ladybug** (VER-6: claims, verdicts, sources, vector
  embeddings, nearest-neighbor search). This task does not implement that schema; it only
  proves the engine opens, accepts a connection, and answers a query.
- This infra/skeleton work is tracked by **VER-13**.

## 3. Decisions

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Ladybug only for v1; Postgres deferred | No auth/user card in v1. Avoid building ahead of the roadmap (YAGNI). |
| 2 | Ladybug hosts the domain incl. verification DB | Graph + vector indices fit VER-6/VER-9; one engine for v1. |
| 3 | Domain-centric (dependency-inverted) layering | Dependencies point inward to a stable `domain` core; HTTP framing and DB choice stay swappable. |
| 4 | No generic `shared/` package | In Go it degrades into a junk drawer with cycles. Shared code gets a named home by purpose (`httpx`, `domain`, `config`). |
| 5 | Accept Ladybug single-writer; isolate behind a repository interface | Ladybug allows one `READ_WRITE` handle per process. The `store/graph` seam lets us extract it into a dedicated service later without touching `service`/`handler`. |
| 6 | Production multi-stage Dockerfile done now | The cgo + native-lib build is the tricky part; solve dev and prod link strategy together. |
| 7 | Backend scope only | Frontend layout is handled per the `nextjs` skill when feature cards land. |

## 4. Hard constraint: Ladybug concurrency

Ladybug (`go-ladybug`, cgo wrapper over the C API) permits only **one `READ_WRITE`
`Database` handle per process**, and no second process may open the same on-disk path.
Connections opened from that single handle are goroutine-safe; the internal transaction
manager serializes writes.

Consequence: the backend's graph-write path is **single-instance**. Backend replicas
cannot all embed Ladybug `READ_WRITE` against the same volume. We accept a single backend
instance and contain the engine behind `domain` repository interfaces so a future
extraction (dedicated graph service over HTTP/gRPC, or a client-server graph DB) changes
only `store/graph`.

## 5. Architecture: layering

```
handler  ->  service  ->  domain  <-  store
(controllers) (business)  (models +    (repository
                           interfaces)  implementations)
```

Dependency rule (enforced by review and the `internal/` boundary):

- `domain` imports nothing internal. Holds models and repository interfaces ("ports").
- `service` depends only on `domain` interfaces. No HTTP types, no Cypher.
- `handler` depends on `service`. Translates HTTP to/from domain. No business logic.
- `store/*` implements `domain` interfaces. The only packages that import `lbug`.
- `cmd/server/main.go` is the only place that wires concrete types together.

## 6. Backend layout

Module: `github.com/verovec/truth-in-stream/backend`. Stdlib `net/http`, no framework.
Go 1.26 in-container; `go.mod` `go 1.22` minimum (ServeMux method+wildcard routing, `slog`).

```
stack/backend/
  cmd/server/main.go            # wiring only: config -> store -> services -> handlers -> http.Server + graceful shutdown
  internal/
    config/                     # env -> typed Config; fail-fast on missing required vars
    domain/                     # models + repository interfaces (ports)
    service/                    # business logic; depends on domain interfaces only
    handler/                    # controllers, one file per resource; newMux() router constructor (testable)
    middleware/                 # func(http.Handler) http.Handler: request-id, logging, recovery
    httpx/                      # shared HTTP concerns: JSON encode/decode, error responses
    store/
      graph/                    # ladybug repository; owns the single READ_WRITE handle
  Dockerfile                    # multi-stage: dev + prod targets (cgo/ladybug aware)
  .dockerignore
  go.mod
```

Future seam (not in this task): `store/postgres/` and `migrate/` are added when an auth
card introduces relational data. The layout already reserves their place.

`main.go` startup order: load config -> open Ladybug (`READ_WRITE`, ping via trivial
query) -> build services -> `newMux()` -> `http.Server` with `ReadTimeout`/`WriteTimeout`/
`IdleTimeout` -> `signal.NotifyContext` + `srv.Shutdown(ctx)`, then close Ladybug.

## 7. Data store: Ladybug

- Module: `github.com/LadybugDB/go-ladybug` (`v0.17.0`, engine `v0.17.1`, pre-v1).
- `store/graph` opens one `lbug.OpenDatabase(LADYBUG_PATH, lbug.DefaultSystemConfig())`
  for the process lifetime; opens connections per request; runs a trivial Cypher query as
  a health probe. No domain graph schema in this task (open + ping only; VER-6 owns the
  verification schema).
- Native lib: not bundled. Fetched at build via the upstream `download-liblbug.sh`
  (`go:generate`). Build needs `gcc` + `libstdc++-dev`; runtime needs `libstdc++6`.

## 8. Dev runtime (docker-compose.yml)

Services:

- `frontend` - `node:22-alpine`, unchanged (`npm run dev`, port 3000).
- `backend` - **build from `stack/backend/Dockerfile` target `dev`** (replaces the stock
  `golang:1.26` image). Dev stage installs `gcc libstdc++-dev curl ca-certificates`, runs
  `go generate ./...` to fetch `liblbug.so`, then `go run ./cmd/server`. Port 8080.
  Env: `PORT=8080`, `LADYBUG_PATH=/data/ladybug`. `CGO_LDFLAGS` / rpath set so the linker
  finds `liblbug.so`.

No `postgres` service in this task (deferred).

Volumes: `ladybug-data` (mounted at `/data`; the engine creates `/data/ladybug` itself,
since Ladybug opens a fresh leaf under an existing parent rather than an existing empty
dir), `go-mod-cache`, `frontend-node-modules`, and a cached host `lib-ladybug`.

`GET /healthz` returns 200 when Ladybug responds to a probe query; otherwise 503. This is
the verifiable "stack is wired" signal.

## 9. Production Dockerfile (multi-stage)

Same `stack/backend/Dockerfile`:

- `builder`: `golang:1.26`, install `gcc libstdc++-dev curl`, fetch the **static** Ladybug
  lib (`LBUG_LIB_KIND=static` via the download script), `CGO_ENABLED=1` `go build` linking
  `liblbug.a` + `-lstdc++ -lm`. Produces a binary not requiring `liblbug.so` at runtime.
- `prod` (final): `debian:bookworm-slim` (or distroless/cc) with `libstdc++6` and
  `ca-certificates`; copy the binary. Non-root user. Pure-`scratch` is not possible because
  of the C++ runtime dependency.

AWS provisioning and the deploy pipeline are **out of scope**.

## 10. Pinned versions

`go-ladybug v0.17.0` (engine v0.17.1) · Go 1.26 in-container (`go.mod` `go 1.22`) ·
`node:22-alpine`.

## 11. Testing

- `service` tested against `domain` interfaces with in-memory fakes (no engine needed).
- `handler` tests via `net/http/httptest` against `newMux()`.
- Table-driven tests; CI runs `go test -race ./...`.
- Ladybug open/ping covered by a smoke test gated on the native lib being present.

## 12. Out of scope / follow-ups

- Postgres + user/auth (added when an auth card exists; seam reserved).
- Verification DB schema + ingestion (VER-6) and the embedding/match service (VER-9).
- Transcription, orchestration, player, panel, demo (VER-7, VER-8, VER-10, VER-11, VER-12).
- AWS provisioning + Terraform wiring + deploy pipeline.
- Frontend folder restructuring (per `nextjs` skill when feature cards land).
- Go hot-reload for the backend dev loop (currently `go run`; revisit if cgo rebuild cost
  becomes painful).

## 13. Risks

- `go-ladybug` is pre-v1; API may shift. Contained by the `store/graph` seam.
- cgo + native lib increases build complexity and rules out a static `scratch` image.
- Single-writer Ladybug caps horizontal scaling of the backend; accepted, seam isolates it.
- **Supply-chain (open follow-up):** the `go:generate` native-lib fetch pipes a script from
  a mutable branch (`refs/heads/main`) into `bash` with no integrity check, and the script
  downloads a prebuilt binary. Hardening: pin the script to a commit SHA + verify its
  SHA-256 (or vendor it), and pin the downloaded artifact's SHA-256, with a documented
  rotation procedure. Flagged by automated security review on the VER-13 branch; to resolve
  before treating the build path as production-grade.
