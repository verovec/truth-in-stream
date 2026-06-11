# Local development entrypoint. One command brings the whole stack up with a
# realistic, fully offline dataset; the reset/seed targets refill it in seconds.
# Low-level backend tasks (sqlc, migrate, lint, test) live in stack/backend/Makefile.

COMPOSE    := docker compose
# DSN as seen from inside the compose network (service name `postgres`), used by
# the one-shot migrate container the reset targets drive.
COMPOSE_DB := postgres://postgres:dev@postgres:5432/truthinstream?sslmode=disable

# Wikipedia corpus embed tuning for the containerized wikisync run. The defaults
# are gentle - small batches, low concurrency, a generous per-request timeout,
# and no time box - so a constrained Voyage tier completes without timing out;
# raise WIKI_EMBED_BATCH_SIZE / WIKI_EMBED_CONCURRENCY on a higher tier for speed.
# Watch embed_duration in the streamed logs: when it nears WIKI_EMBED_HTTP_TIMEOUT,
# lower the batch/concurrency or raise the timeout.
WIKI_CORPUS             ?= simplewiki
WIKI_MAX_DURATION       ?= 0
WIKI_EMBED_BATCH_SIZE   ?= 32
WIKI_EMBED_CONCURRENCY  ?= 2
WIKI_EMBED_HTTP_TIMEOUT ?= 300s
WIKI_ENV := WIKI_CORPUS=$(WIKI_CORPUS) WIKI_MAX_DURATION=$(WIKI_MAX_DURATION) WIKI_EMBED_BATCH_SIZE=$(WIKI_EMBED_BATCH_SIZE) WIKI_EMBED_CONCURRENCY=$(WIKI_EMBED_CONCURRENCY) WIKI_EMBED_HTTP_TIMEOUT=$(WIKI_EMBED_HTTP_TIMEOUT)

.PHONY: help up down reset reset-hard seed seed-claims seed-wiki seed-demo seed-videos refresh-embeddings wiki-populate wiki-update migrate logs ps

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

seed: ## Seed every dataset (claims, wiki, demo, sample videos) from the committed cache; idempotent
	$(COMPOSE) run --rm seed

seed-claims: ## Seed only the curated claims
	$(COMPOSE) run --rm seed go run ./cmd/seed -claims

seed-wiki: ## Seed only the Wikipedia evidence subset
	$(COMPOSE) run --rm seed go run ./cmd/seed -wiki

seed-demo: ## Seed only the demo-video results
	$(COMPOSE) run --rm seed go run ./cmd/seed -demo

seed-videos: ## Seed only the curated sample videos (records + best-effort media); SAMPLE_VIDEO_URL overrides the clip
	$(COMPOSE) run --rm seed go run ./cmd/seed -videos

refresh-embeddings: ## Regenerate the committed embedding cache from fixtures via Voyage (needs EMBEDDING_API_KEY)
	$(COMPOSE) run --rm seed go run ./cmd/seed -refresh

wiki-populate: ## Bulk-ingest+embed the full Wikipedia corpus in the foreground (streams logs; resumable, needs EMBEDDING_API_KEY). Tune with WIKI_EMBED_* / WIKI_MAX_DURATION
	$(WIKI_ENV) $(COMPOSE) --profile wiki run --rm wiki-populate

wiki-update: ## Incrementally update the embedded Wikipedia corpus via the MediaWiki API (delta sync, foreground; needs EMBEDDING_API_KEY)
	$(WIKI_ENV) $(COMPOSE) --profile wiki run --rm wiki-populate go run ./cmd/wikisync -mode=delta

migrate: ## Apply all up migrations to the running Postgres
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up

logs: ## Tail logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps
