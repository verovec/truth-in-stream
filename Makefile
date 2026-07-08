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

# Main-account DNS terraform root. Applied manually by the operator against the
# main account (it owns the jeminforme.fr hosted zone) and excluded from CI.
TF_MAIN    := stack/terraform/main-account

# Worker count for the ingestion fleet target (`make fleet-up`). A small default
# keeps a first run cheap; raise it per run as a make argument or shell variable
# (`make fleet-up EMBEDWORKER_REPLICAS=4`) to drain the queue faster on a higher
# Voyage tier. Read by Make, not Compose, so a value in `.env` alone is ignored.
EMBEDWORKER_REPLICAS ?= 2

# Worker count for the category-crawl consumer fleet (`make crawl-workers`).
# Same shape as EMBEDWORKER_REPLICAS: read by Make, raise per run, e.g.
# `make crawl-workers CRAWLWORKER_REPLICAS=4`.
CRAWLWORKER_REPLICAS ?= 2

# Number of parallel crawl producers for `make crawl`. 1 (default) runs a single
# foreground producer over the whole CRAWL_CATEGORIES list (the original
# behavior). N > 1 fans the one list out across N detached producers, each
# crawling a disjoint round-robin slice of the categories - so the single .env
# list stays the source of truth while the crawl runs in parallel. Read by Make,
# raise per run, e.g. `make crawl CRAWL_SHARDS=4`.
CRAWL_SHARDS ?= 1

# Wikipedia corpus keys and embed tuning come from the root .env (gitignored)
# and any shell override, both read by Compose itself: every WIKI_* knob and
# EMBEDDING_API_KEY is interpolated in docker-compose.yml as ${VAR:-default},
# so Compose resolves them shell > .env > default with no manual export. The
# gentle defaults (small batches, low concurrency, generous timeout, run to
# completion) live next to those references in docker-compose.yml. Override per
# run with the environment form, e.g. WIKI_EMBED_BATCH_SIZE=128 make wiki-populate.

# Worker count for the fact-check-archive consumer fleet (`make factcheck-workers`).
FACTCHECKWORKER_REPLICAS ?= 2

# Worker count for the scrutins-archive consumer fleet (`make scrutins-workers`).
SCRUTINSWORKER_REPLICAS ?= 2

# Knobs for `make digest`. MODE empty (default) posts the Block Kit digest to
# SLACK_DIGEST_WEBHOOK_URL; MODE=terminal prints the full untruncated report to
# stdout; MODE=dry-run prints the Slack JSON without posting. EPIC=VER-93 recaps
# a finished epic instead of the daily window. Both map to the cmd/digest flags
# of the same name, e.g. `make digest EPIC=VER-93 MODE=dry-run`.
MODE ?=
EPIC ?=
DIGEST_FLAG := $(if $(MODE),--$(MODE),) $(if $(EPIC),--epic $(EPIC),)

# Target environment for `make push-secrets` and the on-demand ingestion targets
# (crawler/consumer, insee-idempotency-check). prod by default; set ENV=dev to
# drive the dev environment instead (the ingestion hosts live in dev). Exported so
# the scripts under scripts/ read it as ENVIRONMENT.
ENV ?= prod

# Source and action for the on-demand ingestion-host commands (`make crawler` /
# `make consumer`). SOURCE selects the pipeline (wikipedia | stats | factcheck |
# scrutins); ACTION is up | down | status. These run the producer/worker on a
# stop/start-able EC2 host over SSM (scripts/ingest-host.sh) - the hosts live in
# dev today, so pass ENV=dev. DRY_RUN=1 prints the AWS calls without touching
# infra. Non-secret producer config (CRAWL_CATEGORIES, FACTCHECK_QUERIES) is read
# from the environment and forwarded; secrets come from Secrets Manager.
SOURCE ?= wikipedia
ACTION ?= up

.PHONY: help doctor bootstrap up down reset reset-hard backup restore db-tunnel db-push seed seed-claims seed-wiki seed-videos stats-ingest refresh-embeddings fleet-up fleet-down wiki-populate wiki-update wiki-cluster wiki-verify reingest crawl crawl-workers factcheck-crawl factcheck-workers scrutins-crawl scrutins-workers prime keycloak migrate logs ps digest tf-main-account-plan tf-main-account-apply push-secrets crawler consumer insee-idempotency-check secret-scan install-hooks

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

up: ## Bring up the full stack: Postgres+pgvector, migrate, seed (offline), backend, frontend, and local Keycloak (imports stack/keycloak/realm.json on :8081)
	@sh scripts/doctor.sh --quiet
	$(COMPOSE) up --build

keycloak: ## Bring up only the local Keycloak IdP on :8081, importing stack/keycloak/realm.json (admin/guest roles, guest default, the truth-in-stream-web OIDC client). Part of `make up`; see docs/configuration.md for the issuer and dev credentials
	$(COMPOSE) up --build keycloak

down: ## Stop the stack, keeping the Postgres volume
	$(COMPOSE) down --remove-orphans

reset: ## Soft reset: drop the schema, re-migrate, and reseed (container stays up)
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" drop -f
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up
	$(COMPOSE) run --rm seed
	@echo "reset complete: schema rebuilt and dataset reseeded"

reset-hard: ## Hard reset: discard the Postgres volume and rebuild everything from scratch
	$(COMPOSE) down -v --remove-orphans
	$(COMPOSE) up --build
	@echo "hard reset complete: fresh volume, migrated and seeded"

backup: ## Snapshot the database to a timestamped dump under backups/ and upload it to S3 (set DB_BACKUP_BUCKET); preserves embeddings so a reset needs no re-embed
	./scripts/db-backup.sh

restore: ## Restore the database from a local dump (FILE=path) or the latest S3 backup; replaces schema and data, embeddings included
	./scripts/db-restore.sh $(FILE)

db-tunnel: ## Open an SSM port-forward through the bastion to the private RDS (ENV=prod default; PORT overrides the local port). Keep it running; load with `make db-push` in a second terminal
	./scripts/db-tunnel.sh $(ENV) $(if $(PORT),--port $(PORT),)

db-push: ## Load the local embedded DB into RDS over an open `make db-tunnel` tunnel (ENV=prod default; PORT must match the tunnel; FILE=dump to reuse a dump). Vectors transfer via text COPY, halfvec-safe
	./scripts/db-push.sh $(ENV) $(if $(PORT),--port $(PORT),) $(if $(FILE),--file $(FILE),)

seed: ## Seed every dataset (claims, wiki, sample videos) from the committed cache; idempotent
	$(COMPOSE) run --rm seed

seed-claims: ## Seed only the curated claims
	$(COMPOSE) run --rm seed go run ./cmd/seed -claims

seed-wiki: ## Seed only the Wikipedia evidence subset
	$(COMPOSE) run --rm seed go run ./cmd/seed -wiki

seed-videos: ## Seed only the curated sample videos (records + best-effort media); SAMPLE_VIDEO_URL overrides the clip
	$(COMPOSE) run --rm seed go run ./cmd/seed -videos

stats-ingest: ## Bulk-into-live ingest of the official statistical sources (Eurostat + interior-ministry open-data CSV + INSEE): render French passages, upsert them un-embedded under one corpus per source, and enqueue one embedding job per passage; the running worker fleet fills the vectors in place (no inline Voyage call), so the passages are searchable as they embed. Publishes and exits. Idempotent (re-run to refresh, no duplicates; only un-embedded rows republished). Reuses a fleet from `make fleet-up`, or brings up the broker and one worker; scale throughput with `make fleet-up EMBEDWORKER_REPLICAS=N` first. Sources need no key; optional INSEE_API_KEY from .env
	@$(COMPOSE) --profile wiki ps -q rabbitmq | grep -q . || $(COMPOSE) --profile wiki up -d rabbitmq embedworker
	$(COMPOSE) --profile tools run --rm --no-deps stats-ingest

refresh-embeddings: ## Regenerate the committed embedding cache from fixtures via Voyage (needs EMBEDDING_API_KEY)
	$(COMPOSE) run --rm seed go run ./cmd/seed -refresh

fleet-up: ## Start the ingestion consumer: the broker plus a long-running embedding worker fleet (EMBEDWORKER_REPLICAS=2, overridable). Run `make wiki-populate` afterwards to fill the queue; the running fleet drains it. Paid worker, opt-in (wiki profile) - plain `make up` never starts it
	$(COMPOSE) --profile wiki up --scale embedworker=$(EMBEDWORKER_REPLICAS) rabbitmq embedworker
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
	$(COMPOSE) --profile tools run --rm --no-deps wiki-populate

wiki-update: ## Incrementally update the embedded Wikipedia corpus via the MediaWiki API (delta sync, foreground; keys/tuning from .env)
	$(COMPOSE) --profile tools run --rm wiki-populate go run ./cmd/wikisync -mode=delta

wiki-cluster: ## Cluster the embedded corpus into topics and score importance so the next ingest embeds the most important content first (idempotent; run after embedding; WIKI_CLUSTER_* tuning from .env)
	$(COMPOSE) --profile tools run --rm wiki-cluster

wiki-verify: ## Report the live corpus's embedded coverage and verify consistency over the embedded rows (chunks present, no zero vectors, 1024-dim, metadata populated, HNSW index live); exits non-zero on a real defect. It no longer requires 100% embedded - a bulk-into-live corpus fills in over time, so coverage is reported, not gated.
	$(COMPOSE) --profile tools run --rm wiki-verify

reingest: ## Full local corpus reingest: reset corpus+checkpoint, then an ATOMIC bulk rebuild (build in staging, the fleet drains it, swap live) so the corpus is complete before clustering, then cluster and verify. Brings up the broker and one worker; resumable, reuses the on-disk dump; needs EMBEDDING_API_KEY and WIKI_* tuning from .env. Long (paid embed) - run it unattended; a green verify means the corpus is built and consistent.
	$(COMPOSE) --profile wiki up -d rabbitmq embedworker
	$(COMPOSE) --profile tools run --rm wiki-reset
	$(COMPOSE) --profile tools run --rm --no-deps wiki-populate go run ./cmd/wikisync -mode=bulk -atomic -dir=/wiki-dump
	$(COMPOSE) --profile tools run --rm wiki-cluster
	$(COMPOSE) --profile tools run --rm wiki-verify
	@echo "reingest complete: corpus rebuilt (atomic), clustered, and verified"

crawl-workers: ## Start N category-crawl consumers that drain the crawl queue into live wiki_chunks (CRAWLWORKER_REPLICAS=2, overridable). Run `make crawl` afterwards to fill the queue; the running fleet drains it. Paid worker, opt-in (wiki profile)
	$(COMPOSE) --profile wiki up --scale crawlworker=$(CRAWLWORKER_REPLICAS) rabbitmq crawlworker
	@echo "crawl fleet up: rabbitmq + $(CRAWLWORKER_REPLICAS) crawlworker(s) running; run 'make crawl CRAWL_CATEGORIES=...' to fill the queue, watch the drain at http://localhost:15672 (app/dev)"

factcheck-workers: ## Start N fact-check-archive consumers that drain the fact-check queue into the curated political claim DB (FACTCHECKWORKER_REPLICAS=2, overridable). Run `make factcheck-crawl` afterwards to fill the queue; the running fleet drains it. Paid worker (embeds), opt-in (factcheck profile)
	$(COMPOSE) --profile factcheck up --scale factcheckworker=$(FACTCHECKWORKER_REPLICAS) rabbitmq factcheckworker
	@echo "factcheck fleet up: rabbitmq + $(FACTCHECKWORKER_REPLICAS) factcheckworker(s) running; run 'make factcheck-crawl FACTCHECK_QUERIES=...' to fill the queue, watch the drain at http://localhost:15672 (app/dev)"

factcheck-crawl: ## Run the fact-check-archive producer: read already-checked French claims from the Google Fact Check Tools API for FACTCHECK_QUERIES and publish curated-claim jobs, then exit (DB-free). Requires FACTCHECK_API_KEY and FACTCHECK_QUERIES (e.g. `make factcheck-crawl FACTCHECK_QUERIES="retraites,chômage"`); the factcheckworker fleet embeds and upserts them. Reuses a fleet from `make factcheck-workers`, or brings up the broker and one worker
	@$(COMPOSE) --profile factcheck ps -q rabbitmq | grep -q . || $(COMPOSE) --profile factcheck up rabbitmq factcheckworker
	$(COMPOSE) --profile tools run --rm --no-deps factcheckcrawl

scrutins-workers: ## Start N scrutins consumers that drain the scrutins queue into the voting store (SCRUTINSWORKER_REPLICAS=2, overridable). Run `make scrutins-crawl` afterwards to fill the queue; the running fleet drains it. No API key (parse + upsert only), opt-in (scrutins profile)
	$(COMPOSE) --profile scrutins up --scale scrutinsworker=$(SCRUTINSWORKER_REPLICAS) rabbitmq scrutinsworker
	@echo "scrutins fleet up: rabbitmq + $(SCRUTINSWORKER_REPLICAS) scrutinsworker(s) running; run 'make scrutins-crawl' to fill the queue, watch the drain at http://localhost:15672 (app/dev)"

scrutins-crawl: ## Run the scrutins-archive producer: conditionally download the Assemblee Nationale Scrutins.json.zip (skipped when unchanged via a persisted ETag/Last-Modified marker), discover each scrutin, and publish one job per scrutin, then exit (DB-free). No required env (SCRUTINS_LEGISLATURE defaults to 17); the scrutinsworker fleet parses and upserts them. Reuses a fleet from `make scrutins-workers`, or brings up the broker and one worker
	@$(COMPOSE) --profile scrutins ps -q rabbitmq | grep -q . || $(COMPOSE) --profile scrutins up rabbitmq scrutinsworker
	$(COMPOSE) --profile tools run --rm --no-deps scrutinscrawl

# Like wiki-populate: when a crawl fleet is already up, connect with --no-deps so
# the producer does not reconcile (shrink) it; otherwise bring up the broker and
# a single worker first so a bare `make crawl` still drains. CRAWL_SHARDS>1 fans
# the one CRAWL_CATEGORIES list out across N parallel producers (each a disjoint
# round-robin slice), running them to completion and removing each on exit.
crawl: ## Run the category-crawl producer: walk CRAWL_CATEGORIES over the Action API and publish chunk jobs, then exit (DB-free). Requires CRAWL_CATEGORIES (e.g. `make crawl CRAWL_CATEGORIES="Category:Physics"`); the crawl worker fleet embeds and upserts them into live wiki_chunks. Reuses a fleet from `make crawl-workers`, or brings up the broker and one worker. CRAWL_SHARDS=N runs N producers in parallel over disjoint slices of the same list (e.g. `make crawl CRAWL_SHARDS=4`)
	@case "$(CRAWL_SHARDS)" in ''|*[!0-9]*) echo "CRAWL_SHARDS must be a positive integer, got '$(CRAWL_SHARDS)'" >&2; exit 1 ;; esac
	@$(COMPOSE) --profile wiki ps -q rabbitmq | grep -q . || $(COMPOSE) --profile wiki up rabbitmq crawlworker
	@if [ "$(CRAWL_SHARDS)" -le 1 ]; then \
		$(COMPOSE) --profile tools run --rm --no-deps wikicrawl; \
	else \
		pids=""; i=0; \
		while [ $$i -lt $(CRAWL_SHARDS) ]; do \
			$(COMPOSE) --profile tools run --rm --no-deps -T -e CRAWL_SHARDS=$(CRAWL_SHARDS) -e CRAWL_SHARD_INDEX=$$i wikicrawl & \
			pids="$$pids $$!"; i=$$((i + 1)); \
		done; \
		echo "crawl: launched $(CRAWL_SHARDS) parallel shards over CRAWL_CATEGORIES; watch the drain at http://localhost:15672 (app/dev)"; \
		rc=0; for p in $$pids; do wait $$p || rc=1; done; exit $$rc; \
	fi

prime: ## Bring up the paid wiki stack and auto-prime the broker: starts the broker + worker fleet + a one-shot crawl that fills the queue from CRAWL_CATEGORIES (gate on by default - set CHECKWORTHY_API_KEY, or CRAWL_CHECKWORTHY=false, in .env), then the fleet drains it. Plain `make up` stays free (this is the wiki profile). Equivalent to `docker compose --profile wiki up -d` (builds the image if missing).
	$(COMPOSE) --profile wiki up
	@echo "wiki stack up: broker + worker fleet + a one-shot prime crawl (CRAWL_CATEGORIES). Watch the queue fill and drain at http://localhost:15672 (app/dev); 'make crawl-workers CRAWLWORKER_REPLICAS=N' scales the drain."

push-secrets: ## Push the allowlisted app secrets from the local .env into AWS Secrets Manager under <project>/<ENV>/app/ (ENV=prod default; ENV=dev for dev). Idempotent (describe-then-create-or-put), reads .env at runtime only, and never echoes a value: each is handed to the CLI as a chmod-600 file:// reference and shredded. Pushes only the runtime app keys (EMBEDDING_API_KEY, TRANSCRIPTION_API_KEY, AUTH_EMAIL, AUTH_PASSWORD_HASH, SESSION_SECRET, DEEPSEEK_API_KEY, GEMINI_API_KEY, SLACK_WEBHOOK_URL); the terraform-owned DATABASE_URL and RABBITMQ_URL are never touched. Run after `terraform apply` creates the empty secret containers; prod asks you to type the env name to confirm.
	./scripts/push-secrets.sh $(ENV)

crawler: ## Run a source's producer on the crawler EC2 host over SSM (SOURCE=wikipedia default; ACTION=up|down|status). Starts the host if stopped, fills the queue, streams output, surfaces the exit code; add --stop-after to stop the host after the run. Non-secret producer config (CRAWL_CATEGORIES, FACTCHECK_QUERIES) comes from the environment. DRY_RUN=1 prints the AWS calls. Hosts live in dev, so pass ENV=dev
	ENVIRONMENT=$(ENV) ./scripts/ingest-host.sh crawler $(SOURCE) $(ACTION)

consumer: ## Bring a source's worker up on the consumer EC2 host over SSM to drain its queue into the DB (SOURCE=wikipedia default; ACTION=up|down|status). `down` stops the host for cost control; `status` reports state + queue depth. Secrets come from Secrets Manager on the host. DRY_RUN=1 prints the AWS calls. Hosts live in dev, so pass ENV=dev
	ENVIRONMENT=$(ENV) ./scripts/ingest-host.sh consumer $(SOURCE) $(ACTION)

insee-idempotency-check: ## INSEE re-run idempotency checkpoint against the real RDS: count INSEE passages, re-run statsingest, assert the count did not grow (no duplicate passages). Proves the VER-123/124 provenance key is idempotent on real RDS. Run over an open `make db-tunnel` tunnel (psql to localhost). SKIP_INGEST=1 counts back-to-back without re-ingesting; DRY_RUN=1 dry-runs the re-ingest. ENV=prod default
	ENVIRONMENT=$(ENV) ./scripts/insee-idempotency-check.sh

migrate: ## Apply all up migrations to the running Postgres
	$(COMPOSE) run --rm migrate -path=/migrations -database "$(COMPOSE_DB)" up

logs: ## Tail logs for all services
	$(COMPOSE) logs -f

ps: ## Show service status
	$(COMPOSE) ps

digest: ## Post the dev digest (cards shipped in the last 24h, the project's remaining work, open PRs, stalled cards) to SLACK_DIGEST_WEBHOOK_URL. `make digest EPIC=VER-93` recaps a finished epic instead; `make digest MODE=terminal` prints the full report to stdout; `make digest MODE=dry-run` prints the Slack JSON without posting. Reads SLACK_DIGEST_WEBHOOK_URL, LINEAR_API_KEY, LINEAR_PROJECT, GITHUB_TOKEN, GITHUB_REPO, DIGEST_SUMMARY_API_KEY, DIGEST_SUMMARY_MODEL, LLM_PROVIDER, DEEPSEEK_API_KEY, GEMINI_API_KEY from .env; any missing one degrades that section to a note (or shipped cards to their titles)
	@set -a; [ -f .env ] && eval "$$(grep -E '^(SLACK_DIGEST_WEBHOOK_URL|LINEAR_API_KEY|LINEAR_PROJECT|GITHUB_TOKEN|GITHUB_REPO|DIGEST_SUMMARY_API_KEY|DIGEST_SUMMARY_MODEL|LLM_PROVIDER|DEEPSEEK_API_KEY|GEMINI_API_KEY)=' .env)"; set +a; \
	  $(GO) -C stack/backend run ./cmd/digest $(DIGEST_FLAG)

tf-main-account-plan: ## Plan the main-account DNS root (ACM validation + apex/www CloudFront aliases). Read-only; needs main-account credentials (or main_account_role_arn). See stack/terraform/main-account/README.md
	cd $(TF_MAIN) && terraform init && terraform plan

tf-main-account-apply: ## Apply the main-account DNS root by hand (operator only; CI never runs this). Publishes the ACM validation records and apex/www CloudFront aliases into the jeminforme.fr zone. See stack/terraform/main-account/README.md
	cd $(TF_MAIN) && terraform init && terraform apply

secret-scan: ## Scan the tracked tree for committed AWS account ids (this repo is public). Fails on any unrecognized 12-digit account-shaped token outside the allow-list. Same check CI runs on every PR
	./scripts/secret-scan.sh

install-hooks: ## Point core.hooksPath at .githooks so the account-id scan runs before every local commit (opt-in, per-clone)
	git config core.hooksPath .githooks
	@echo "hooks installed: .githooks (pre-commit runs scripts/secret-scan.sh)"
