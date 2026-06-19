# Backend developer tasks. Run from stack/backend.
# Tooling is pinned and run via Docker so no local install is required.

SQLC_IMAGE    := sqlc/sqlc:1.31.1
MIGRATE_IMAGE := migrate/migrate:v4.18.3
# Override for a real target, e.g. make migrate-up DATABASE_URL=postgres://...
DATABASE_URL  ?= postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable
# Export the default so the go-run targets (seed, ingest, wikisync) see it in
# their environment, not just the migrate-up interpolation.
export DATABASE_URL

# Wall-clock budget for one wiki-populate run. The corpus embed is long and
# paid, so a run stops cleanly at the budget and the next run resumes; populate
# over several runs. Override per invocation, e.g. make wiki-populate WIKI_MAX_DURATION=30m.
WIKI_MAX_DURATION ?= 15m
# Persisted across runs so a resume reuses the downloaded dump dir.
WIKI_DUMP_DIR     ?= /tmp/wikisync-dump

.PHONY: help sqlc sqlc-verify migrate-up migrate-down fmt vet test test-integration tidy build lambda-mqmetrics ingest stats-ingest seed scrutins-ingest refresh-embeddings wikisync wiki-populate wiki-cluster wiki-verify digest

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  %-16s %s\n", $$1, $$2}'

sqlc: ## Regenerate type-safe query code from queries/ + migrations/
	docker run --rm -v "$(CURDIR)":/src -w /src $(SQLC_IMAGE) generate

sqlc-verify: ## Fail if generated code is out of sync with the SQL (CI guard)
	docker run --rm -v "$(CURDIR)":/src -w /src $(SQLC_IMAGE) diff

migrate-up: ## Apply all up migrations to DATABASE_URL
	docker run --rm -v "$(CURDIR)/migrations":/migrations $(MIGRATE_IMAGE) -path=/migrations -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the most recent migration
	docker run --rm -v "$(CURDIR)/migrations":/migrations $(MIGRATE_IMAGE) -path=/migrations -database "$(DATABASE_URL)" down 1

fmt: ## Format all Go code
	gofmt -w .

vet: ## go vet
	go vet ./...

test: ## Unit tests (store integration tests skip without TEST_DATABASE_URL)
	go test -race ./...

test-integration: ## Run all tests, pointing the store integration tests at DATABASE_URL
	TEST_DATABASE_URL="$(DATABASE_URL)" go test -race ./...

tidy: ## go mod tidy
	go mod tidy

build: ## Static build of the server
	CGO_ENABLED=0 go build ./...

lambda-mqmetrics: ## Build the mqmetrics lambda bootstrap (provided.al2023, arm64) for terraform to package and deploy
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o build/mqmetrics/bootstrap ./cmd/mqmetrics

lambda-workerlifecycle: ## Build the worker-lifecycle lambda bootstrap (provided.al2023, arm64) for terraform to package and deploy
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o build/workerlifecycle/bootstrap ./cmd/workerlifecycle

ingest: ## Embed and upsert the seed claims (needs DATABASE_URL and EMBEDDING_API_KEY)
	go run ./cmd/ingest

stats-ingest: ## Fetch the curated Eurostat series, render French passages, embed and upsert into the evidence corpus; idempotent, no API key for the source (needs DATABASE_URL and EMBEDDING_API_KEY)
	go run ./cmd/statsingest

seed: ## Load the local dataset (claims, wiki, sample videos) offline from the committed cache (DATABASE_URL; no API key)
	go run ./cmd/seed

scrutins-ingest: ## Bulk-ingest Assemblee Nationale open-data scrutins into the voting-record store; idempotent (DATABASE_URL; SCRUTINS_DIR = unzipped Scrutins.json.zip)
	go run ./cmd/scrutinsingest -dir=$(SCRUTINS_DIR)

refresh-embeddings: ## Regenerate the committed embedding cache from fixtures via Voyage (needs EMBEDDING_API_KEY)
	go run ./cmd/seed -refresh

wikisync: ## Download and ingest the Wikipedia corpus (WIKI_CORPUS, DATABASE_URL)
	go run ./cmd/wikisync

wiki-populate: ## Bulk-ingest+embed the Wikipedia corpus for up to WIKI_MAX_DURATION, then stop; resumable, so re-run to continue (WIKI_CORPUS, DATABASE_URL, EMBEDDING_API_KEY)
	@mkdir -p $(WIKI_DUMP_DIR)
	go run ./cmd/wikisync -mode=bulk -max-duration=$(WIKI_MAX_DURATION) -dir=$(WIKI_DUMP_DIR)

wiki-cluster: ## Cluster the embedded corpus into topics and score importance, written back so the next ingest embeds the most important content first (idempotent; run after embedding; WIKI_CLUSTER_* tuning, DATABASE_URL)
	go run ./cmd/wikicluster

wiki-verify: ## Verify the live corpus is fully rebuilt: chunks present, every chunk embedded (non-null, non-zero, 1024-dim), metadata populated, HNSW index live; exits non-zero on any defect (DATABASE_URL)
	go run ./cmd/wikiverify

digest: ## Post the daily development digest to Slack (needs SLACK_DIGEST_WEBHOOK_URL)
	go run ./cmd/digest
