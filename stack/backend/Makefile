# Backend developer tasks. Run from stack/backend.
# Tooling is pinned and run via Docker so no local install is required.

SQLC_IMAGE    := sqlc/sqlc:1.31.1
MIGRATE_IMAGE := migrate/migrate:v4.18.3
# Override for a real target, e.g. make migrate-up DATABASE_URL=postgres://...
DATABASE_URL  ?= postgres://postgres:dev@localhost:5432/truthinstream?sslmode=disable
# Export the default so the go-run targets (seed, ingest, wikisync) see it in
# their environment, not just the migrate-up interpolation.
export DATABASE_URL

.PHONY: help sqlc sqlc-verify migrate-up migrate-down fmt vet test test-integration tidy build ingest seed refresh-embeddings wikisync digest

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

ingest: ## Embed and upsert the seed claims (needs DATABASE_URL and EMBEDDING_API_KEY)
	go run ./cmd/ingest

seed: ## Load the local dataset (claims, wiki, demo) offline from the committed cache (DATABASE_URL; no API key)
	go run ./cmd/seed

refresh-embeddings: ## Regenerate the committed embedding cache from fixtures via Voyage (needs EMBEDDING_API_KEY)
	go run ./cmd/seed -refresh

wikisync: ## Download and ingest the Wikipedia corpus (WIKI_CORPUS, DATABASE_URL)
	go run ./cmd/wikisync

digest: ## Post the daily development digest to Slack (needs SLACK_DIGEST_WEBHOOK_URL)
	go run ./cmd/digest
