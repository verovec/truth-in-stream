# Local setup

Everything needed to run truth-in-stream on your machine: bring the stack up, work with the data,
run the tests safely, and diagnose the common failures. Production setup lives in
[`first-setup.md`](first-setup.md); this page owns local development only.

## Prerequisites

- **Docker Engine 24.0+ with the Compose v2 plugin** — verify with `docker compose version` (the
  `docker compose` subcommand, never the legacy `docker-compose` binary). This is the floor for the
  `profiles`, `--scale`, and healthcheck-`depends_on` features the stack uses.
- **GNU make** and a few GB of free disk.
- **A Go toolchain** (the version in `stack/backend/go.mod`) only if you run the backend tests or Go
  tooling directly on the host; bringing the stack up needs Docker alone.

`make doctor` preflights Docker, the Compose v2 plugin, make, and a running daemon. Run it first if a
command below fails to start anything.

## Bring the stack up

From a clean clone, three commands take you to the demo playing in the browser. The local dataset
(curated claims, a Wikipedia evidence subset, demo-video results) seeds **fully offline** from a
committed embedding cache, so **no API keys are needed** to bring the stack up.

```bash
make doctor      # optional: preflight Docker, Compose v2, make, and the daemon
make bootstrap   # generate .env: operator email, argon2id password hash, session secret (idempotent)
make up          # build and start Postgres, migrate, offline seed, Keycloak, backend, frontend
```

`make up` runs, in order: Postgres+pgvector, a one-shot `migrate`, a one-shot offline `seed`, a local
Keycloak importing a prepopulated realm, then the backend and frontend. Sign in through Keycloak with
a local dev user:

| Surface | URL | Login |
|---------|-----|-------|
| Frontend | <http://localhost:3000> | `admin` / `test1234` or `guest` / `guest` |
| Backend health | <http://localhost:8080/healthz> | — |
| Keycloak admin console | <http://localhost:8081> | — |

`admin` additionally reaches the [backoffice](backoffice.md) (admin-only ingestion) and sees the
debug toggle. To move past the offline demo to live analysis, add real API keys — see
[Configuration](configuration.md).

## Everyday commands

All from the repo root (`make help` lists every target):

| Command | What it does |
|---------|--------------|
| `make up` / `make down` | Start the full stack / stop it, keeping the Postgres volume |
| `make reset` | Soft reset: drop the schema, re-migrate, reseed (container stays up) |
| `make reset-hard` | Discard the Postgres volume and rebuild from scratch |
| `make seed` | Reload the offline dataset (claims, wiki subset, sample videos); idempotent |
| `make logs` / `make ps` | Tail all logs / show service status |
| `make migrate` | Apply up migrations to the running Postgres |

`make reset` leaves the videos gallery empty until the backend restarts (the sample video is seeded
at backend startup) — run `make down && make up`, or restart the backend container, to repopulate it.

Configuration and the full environment-variable inventory live in
[Configuration](configuration.md).

## Optional: the ingestion fleet (paid, opt-in)

The offline demo needs none of this. To ingest more evidence, start the broker + embedding-worker
fleet, then fill the queue:

```bash
make fleet-up EMBEDWORKER_REPLICAS=4    # broker + N embedding workers (opt-in `wiki` profile)
make wiki-populate                      # fill the queue; the running fleet drains it
```

These call paid embedding APIs (they need `EMBEDDING_API_KEY`) and only start under the `wiki`
profile — plain `make up` never starts them. `make fleet-up` runs **attached**, so it tails the
Postgres log; to avoid that firehose, run detached and watch only the workers:

```bash
docker compose --profile wiki up -d --scale embedworker=4 rabbitmq embedworker
docker compose logs -f embedworker
```

The full pipeline (local and cloud, diagrams, sources) is documented in
[Ingestion pipeline](ingestion-pipeline.md).

## Running the tests

The suite is cross-stack; the full local gate and CI contract are in [Development](development.md).
The essentials:

**Unit tests** (no database, hermetic):

```bash
cd stack/backend && make test        # go test -race ./... (DB integration tests skip)
cd stack/frontend && npm test        # vitest
```

**Backend integration tests** — run these from the repo root:

```bash
make itest        # provision a throwaway DB, then go test -race ./... against real pgvector
```

`make itest` creates a dedicated **`truthinstream_test`** database (idempotent; `make test-db` does
just the provisioning), then points `TEST_DATABASE_URL` at it. This matters: the store and seed
integration tests **reset the schema on every run** — they drop every known table and reapply all
migrations — so pointing them at the seeded `truthinstream` dev database would wipe your local data.
`make itest` keeps them on the throwaway database, exactly as CI does with its own `test` database.
Never set `TEST_DATABASE_URL` to the dev database, and never run `go test` against a hand-created
test database that may not exist (see Troubleshooting).

**Shell tooling tests** (stub Docker/AWS; no live services):

```bash
./scripts/doctor.test.sh      # local-stack preflight
./scripts/test-db.test.sh     # throwaway-test-DB provisioning
```

## Troubleshooting

### `FATAL: database "…_test" does not exist` flooding the logs

A wall of `FATAL: database "xyz" does not exist` (often during `make fleet-up`) means an integration
test run was pointed at a **throwaway `TEST_DATABASE_URL` database that does not exist**. Every one of
the ~130 integration tests attempts a connection and fails, and because Postgres logs every client's
failures — and `make fleet-up` runs attached to the Postgres log — the flood surfaces in the
fleet-up output even though the fleet itself is fine.

Fix: run integration tests with **`make itest`**, which creates the database first. Do not
hand-create a per-run test database and set `TEST_DATABASE_URL` to it by hand; if that database is
ever dropped or misnamed, you get this storm. Drop any stale hand-made test database with
`docker compose exec -T postgres dropdb --if-exists <name>`.

### `make fleet-up` output looks broken but the fleet started

`docker compose up` (which `make fleet-up` uses) attaches to the **Postgres** container log, so
errors from *every* client on that database — other test runs, a re-seed hitting existing rows, a
GUI client like DBeaver — appear interleaved in the output even though they are unrelated to the
fleet. Look for the workers' own `embedding worker started` lines to confirm the fleet came up. To
watch only the workers, start detached and tail them (see the ingestion-fleet section above).

### A stack command fails to start

Run `make doctor`. It reports a missing Docker binary, a missing Compose v2 plugin, or a stopped
daemon with the fix for each.
