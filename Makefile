# Local development entrypoint. One command brings the whole stack up with a
# realistic, fully offline dataset; the reset/seed targets refill it in seconds.
# Low-level backend tasks (sqlc, migrate, lint, test) live in stack/backend/Makefile.

COMPOSE    := docker compose
# DSN as seen from inside the compose network (service name `postgres`), used by
# the one-shot migrate container the reset targets drive.
COMPOSE_DB := postgres://postgres:dev@postgres:5432/truthinstream?sslmode=disable

# Go toolchain used by `make bootstrap` to generate the operator credentials.
# Override if `go` is not on PATH, e.g. `make bootstrap GO=/usr/local/go/bin/go`.
GO         ?= go

# Worker count for the ingestion fleet target (`make fleet-up`). A small default
# keeps a first run cheap; raise it per run as a make argument or shell variable
# (`make fleet-up EMBEDWORKER_REPLICAS=4`) to drain the queue faster on a higher
# Voyage tier. Read by Make, not Compose, so a value in `.env` alone is ignored.
EMBEDWORKER_REPLICAS ?= 2

# Wikipedia corpus keys and embed tuning come from the root .env (gitignored)
# and any shell override, both read by Compose itself: every WIKI_* knob and
# EMBEDDING_API_KEY is interpolated in docker-compose.yml as ${VAR:-default},
# so Compose resolves them shell > .env > default with no manual export. The
# gentle defaults (small batches, low concurrency, generous timeout, run to
# completion) live next to those references in docker-compose.yml. Override per
# run with the environment form, e.g. WIKI_EMBED_BATCH_SIZE=128 make wiki-populate.

.PHONY: help doctor bootstrap up down reset reset-hard backup restore seed seed-claims seed-wiki seed-videos refresh-embeddings fleet-up fleet-down wiki-populate wiki-update wiki-cluster wiki-verify reingest migrate logs ps

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-20s %s\n", $$1, $$2}'

doctor: ## Preflight the host for the stack: GNU make, Docker Engine, the Compose v2 plugin, and a running daemon, with a clear remedy for whatever is missing
	@sh scripts/doctor.sh

bootstrap: ## Generate .env on a fresh checkout (operator email, argon2id hash, session secret, demo placeholder keys); idempotent and never stores the plaintext password. Needs the Go toolchain (override with GO=); prompts for any value still unset, or read them from BOOTSTRAP_EMAIL / BOOTSTRAP_PASSWORD
	@command -v $(GO) >/dev/null 2>&1 || { \
	  echo "make bootstrap needs the Go toolchain to generate the operator credentials."; \
	  echo "Install Go (https://go.dev/dl/) or set GO=/path/to/go, or follow the manual"; \
	  echo "Configuration steps in the README to fill .env by hand."; exit 1; }
	@needemail=0; needhash=0; needsecret=0; \
	if [ ! -f .env ]; then needemail=1; needhash=1; needsecret=1; else \
	  exline=$$(grep -m1 '^AUTH_PASSWORD_HASH=' .env.example); \
	  grep -qxF "$$exline" .env && needhash=1; \
	  grep -qE '^AUTH_EMAIL=$$' .env && needemail=1; \
	  grep -qE '^SESSION_SECRET=$$' .env && needsecret=1; \
	fi; \
	email="$${BOOTSTRAP_EMAIL:-}"; password="$${BOOTSTRAP_PASSWORD:-}"; \
	if [ "$$needemail" = 1 ] && [ -z "$$email" ] && [ -t 0 ]; then \
	  printf 'Operator email: '; read -r email; fi; \
	if [ "$$needhash" = 1 ] && [ -z "$$password" ] && [ -t 0 ]; then \
	  printf 'Operator password (input hidden): '; stty -echo 2>/dev/null; read -r password || true; stty echo 2>/dev/null; printf '\n'; fi; \
	BOOTSTRAP_EMAIL="$$email" BOOTSTRAP_PASSWORD="$$password" \
	  $(GO) -C stack/backend run ./cmd/bootstrap -root "$(CURDIR)"

up: ## Bring up the full stack: Postgres+pgvector, migrate, seed (offline), backend, frontend
	@sh scripts/doctor.sh --quiet
	$(COMPOSE) up -d --build

down: ## Stop the stack, keeping the Postgres volume
	$(COMPOSE) down

reset: ## Soft reset: drop the schema, re-migrate, and reseed (container stays up)
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" drop -f
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up
	$(COMPOSE) run --rm seed
	@echo "reset complete: schema rebuilt and dataset reseeded"

reset-hard: ## Hard reset: discard the Postgres volume and rebuild everything from scratch
	$(COMPOSE) down -v
	$(COMPOSE) up -d --build
	@echo "hard reset complete: fresh volume, migrated and seeded"

backup: ## Snapshot the database to a timestamped dump under backups/ and upload it to S3 (set DB_BACKUP_BUCKET); preserves embeddings so a reset needs no re-embed
	./scripts/db-backup.sh

restore: ## Restore the database from a local dump (FILE=path) or the latest S3 backup; replaces schema and data, embeddings included
	./scripts/db-restore.sh $(FILE)

seed: ## Seed every dataset (claims, wiki, sample videos) from the committed cache; idempotent
	$(COMPOSE) run --rm seed

seed-claims: ## Seed only the curated claims
	$(COMPOSE) run --rm seed go run ./cmd/seed -claims

seed-wiki: ## Seed only the Wikipedia evidence subset
	$(COMPOSE) run --rm seed go run ./cmd/seed -wiki

seed-videos: ## Seed only the curated sample videos (records + best-effort media); SAMPLE_VIDEO_URL overrides the clip
	$(COMPOSE) run --rm seed go run ./cmd/seed -videos

refresh-embeddings: ## Regenerate the committed embedding cache from fixtures via Voyage (needs EMBEDDING_API_KEY)
	$(COMPOSE) run --rm seed go run ./cmd/seed -refresh

fleet-up: ## Start the ingestion consumer: the broker plus a long-running embedding worker fleet (EMBEDWORKER_REPLICAS=2, overridable). Run `make wiki-populate` afterwards to fill the queue; the running fleet drains it. Paid worker, opt-in (wiki profile) - plain `make up` never starts it
	$(COMPOSE) --profile wiki up -d --scale embedworker=$(EMBEDWORKER_REPLICAS) rabbitmq embedworker
	@echo "fleet up: rabbitmq + $(EMBEDWORKER_REPLICAS) embedworker(s) running; run 'make wiki-populate' to fill the queue, watch the drain at http://localhost:15672 (app/dev)"

fleet-down: ## Stop the ingestion broker and worker fleet (removes just those containers; the rest of the stack and every named volume, Postgres data included, are left intact)
	$(COMPOSE) --profile wiki rm -sf rabbitmq embedworker
	@echo "fleet down: broker and workers removed; named volumes (Postgres data, broker queue state) preserved"

# `docker compose run` reconciles its dependencies back to their file-defined scale,
# so a plain `run wiki-populate` would shrink a fleet that `make fleet-up` scaled up.
# When the broker is already running (a fleet is up), connect with --no-deps and leave
# the fleet untouched; otherwise start the broker and a single worker first.
wiki-populate: ## Bulk-into-live ingest: write the Wikipedia corpus straight into the live table and enqueue embedding jobs; the running worker fleet fills the vectors in place, so the corpus is searchable as it embeds (no swap). Publishes and exits - watch coverage with `make wiki-verify`. Resumable, reuses an on-disk dump. Reuses a fleet from `make fleet-up`, or brings up the broker and one worker; scale throughput with `make fleet-up EMBEDWORKER_REPLICAS=N` first
	@$(COMPOSE) --profile wiki ps -q rabbitmq | grep -q . || $(COMPOSE) --profile wiki up -d rabbitmq embedworker
	$(COMPOSE) --profile wiki run --rm --no-deps wiki-populate

wiki-update: ## Incrementally update the embedded Wikipedia corpus via the MediaWiki API (delta sync, foreground; keys/tuning from .env)
	$(COMPOSE) --profile wiki run --rm wiki-populate go run ./cmd/wikisync -mode=delta

wiki-cluster: ## Cluster the embedded corpus into topics and score importance so the next ingest embeds the most important content first (idempotent; run after embedding; WIKI_CLUSTER_* tuning from .env)
	$(COMPOSE) --profile wiki run --rm wiki-cluster

wiki-verify: ## Report the live corpus's embedded coverage and verify consistency over the embedded rows (chunks present, no zero vectors, 1024-dim, metadata populated, HNSW index live); exits non-zero on a real defect. It no longer requires 100% embedded - a bulk-into-live corpus fills in over time, so coverage is reported, not gated.
	$(COMPOSE) --profile wiki run --rm wiki-verify

reingest: ## Full local corpus reingest: reset corpus+checkpoint, then an ATOMIC bulk rebuild (build in staging, the fleet drains it, swap live) so the corpus is complete before clustering, then cluster and verify. Brings up the broker and one worker; resumable, reuses the on-disk dump; needs EMBEDDING_API_KEY and WIKI_* tuning from .env. Long (paid embed) - run it unattended; a green verify means the corpus is built and consistent.
	$(COMPOSE) --profile wiki run --rm wiki-reset
	$(COMPOSE) --profile wiki run --rm wiki-populate go run ./cmd/wikisync -mode=bulk -atomic -dir=/wiki-dump
	$(COMPOSE) --profile wiki run --rm wiki-cluster
	$(COMPOSE) --profile wiki run --rm wiki-verify
	@echo "reingest complete: corpus rebuilt (atomic), clustered, and verified"

migrate: ## Apply all up migrations to the running Postgres
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up

logs: ## Tail logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps
