# Local development entrypoint. One command brings the whole stack up with a
# realistic, fully offline dataset; the reset/seed targets refill it in seconds.
# Low-level backend tasks (sqlc, migrate, lint, test) live in stack/backend/Makefile.

COMPOSE    := docker compose
# DSN as seen from inside the compose network (service name `postgres`), used by
# the one-shot migrate container the reset targets drive.
COMPOSE_DB := postgres://postgres:dev@postgres:5432/truthinstream?sslmode=disable

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

.PHONY: help up down reset reset-hard backup restore seed seed-claims seed-wiki seed-videos refresh-embeddings fleet-up fleet-down wiki-populate wiki-update wiki-cluster wiki-verify reingest migrate logs ps

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-20s %s\n", $$1, $$2}'

up: ## Bring up the full stack: Postgres+pgvector, migrate, seed (offline), backend, frontend
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
wiki-populate: ## Bulk-ingest the Wikipedia corpus and enqueue embedding jobs; the worker fleet embeds and the corpus swaps in once drained (resumable, reuses an on-disk dump). Reuses a fleet from `make fleet-up`, or brings up the broker and one worker if none is running; scale throughput with `make fleet-up EMBEDWORKER_REPLICAS=N` first
	@$(COMPOSE) --profile wiki ps -q rabbitmq | grep -q . || $(COMPOSE) --profile wiki up -d rabbitmq embedworker
	$(COMPOSE) --profile wiki run --rm --no-deps wiki-populate

wiki-update: ## Incrementally update the embedded Wikipedia corpus via the MediaWiki API (delta sync, foreground; keys/tuning from .env)
	$(COMPOSE) --profile wiki run --rm wiki-populate go run ./cmd/wikisync -mode=delta

wiki-cluster: ## Cluster the embedded corpus into topics and score importance so the next ingest embeds the most important content first (idempotent; run after embedding; WIKI_CLUSTER_* tuning from .env)
	$(COMPOSE) --profile wiki run --rm wiki-cluster

wiki-verify: ## Verify the live corpus is fully rebuilt (chunks present, every chunk embedded non-null/non-zero/1024-dim, metadata populated, HNSW index live); exits non-zero and logs the failing checks on a partial or stale corpus
	$(COMPOSE) --profile wiki run --rm wiki-verify

reingest: ## Full local corpus reingest under the reworked pipeline: reset corpus+checkpoint, bulk-ingest+enqueue, fleet embeds and swaps live, cluster, then verify. Brings up the broker and one worker; resumable, reuses the on-disk dump; needs EMBEDDING_API_KEY and WIKI_* tuning from .env. Long (paid embed) - run it unattended; a green verify means the corpus is ready.
	$(COMPOSE) --profile wiki run --rm wiki-reset
	$(COMPOSE) --profile wiki run --rm wiki-populate
	$(COMPOSE) --profile wiki run --rm wiki-cluster
	$(COMPOSE) --profile wiki run --rm wiki-verify
	@echo "reingest complete: corpus rebuilt, clustered, and verified"

migrate: ## Apply all up migrations to the running Postgres
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up

logs: ## Tail logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps
