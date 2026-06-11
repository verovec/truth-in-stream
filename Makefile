# Local development entrypoint. One command brings the whole stack up with a
# realistic, fully offline dataset; the reset/seed targets refill it in seconds.
# Low-level backend tasks (sqlc, migrate, lint, test) live in stack/backend/Makefile.

COMPOSE    := docker compose
# DSN as seen from inside the compose network (service name `postgres`), used by
# the one-shot migrate container the reset targets drive.
COMPOSE_DB := postgres://postgres:dev@postgres:5432/truthinstream?sslmode=disable

# Wikipedia corpus keys and embed tuning come from the root .env (gitignored)
# and any shell override, both read by Compose itself: every WIKI_* knob and
# EMBEDDING_API_KEY is interpolated in docker-compose.yml as ${VAR:-default},
# so Compose resolves them shell > .env > default with no manual export. The
# gentle defaults (small batches, low concurrency, generous timeout, run to
# completion) live next to those references in docker-compose.yml. Override per
# run with the environment form, e.g. WIKI_EMBED_BATCH_SIZE=128 make wiki-populate.

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

wiki-populate: ## Bulk-ingest+embed the full Wikipedia corpus in the foreground (streams logs; resumable, reuses an on-disk dump). EMBEDDING_API_KEY and WIKI_* tuning come from .env
	$(COMPOSE) --profile wiki run --rm wiki-populate

wiki-update: ## Incrementally update the embedded Wikipedia corpus via the MediaWiki API (delta sync, foreground; keys/tuning from .env)
	$(COMPOSE) --profile wiki run --rm wiki-populate go run ./cmd/wikisync -mode=delta

migrate: ## Apply all up migrations to the running Postgres
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up

logs: ## Tail logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps
