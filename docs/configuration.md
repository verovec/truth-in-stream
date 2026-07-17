# Configuration and local data

This page covers environment configuration, the auth secrets, and the offline seeded dataset that
`make up` brings up. For the one-command quick start, see the [README](../README.md#quick-start).

## Environment variables

Secrets come from a local `.env` (gitignored); `docker compose` interpolates it. The full set of
tuning knobs lives in `stack/backend/internal/config`. The essentials:

| Variable | Required | Purpose |
|----------|----------|---------|
| `DATABASE_URL` | yes (compose sets a dev value) | Postgres + pgvector connection string |
| `REDIS_URL` | no (compose wires the local `redis` service) | Analysis-cache connection (`redis://` or `rediss://`). Set and reachable -> completed imported/uploaded videos replay instantly; empty, invalid, or unreachable -> silent no-op (normal live pipeline). May carry credentials, so it is never logged |
| `ANALYSIS_CACHE_TTL` | no (default `24h`) | Lifetime of a cached completed analysis; on expiry the video re-analyses through the live pipeline |
| `TRANSCRIPTION_API_KEY` | yes (live only) | AssemblyAI key. `u3-rt-pro` streaming is the single transcriber for live streams and imported videos. Not used by the offline demo |
| `EMBEDDING_API_KEY` | no for seeding | Voyage `voyage-4-large`. Seeding is offline; needed only for live query embedding, `make refresh-embeddings`, or wiki ingestion |
| `EMBEDDING_MODEL` | no (default `voyage-4-large`) | Must match between ingest and query (different models = different vector spaces); pinned to 1024 dims. Changing it requires `make refresh-embeddings` |
| `KEYCLOAK_ISSUER` / `KEYCLOAK_CLIENT_ID` | no (default the local realm) | Keycloak OIDC issuer and authorized party. The `/api` subtree is gated on the verified Keycloak identity in every environment |
| `LEGACY_PASSWORD_LOGIN` | no (default off) | Re-enables the retired password-session login (`/api/login` + `/api/logout`) for an environment that has no Keycloak yet. Off in dev and production; see [Authentication](#authentication-keycloak-identity-gate) |
| `AUTH_EMAIL` / `AUTH_PASSWORD_HASH` / `SESSION_SECRET` | only when `LEGACY_PASSWORD_LOGIN=true` | Legacy operator login (single user), argon2id hash, and HMAC session key (>= 32 bytes). Not read when the legacy login is off |
| `SESSION_TTL` | no (default `24h`) | Legacy session lifetime (only with `LEGACY_PASSWORD_LOGIN=true`) |
| `CORS_ALLOWED_ORIGIN` | no | Leave unset for same-origin dev |
| `PORT` | no (default `8080`) | Backend listen port |
| `DOCUMENT_MAX_SIZE_BYTES` | no (default 30 MB) | Upload-size cap for a PDF ingested from the backoffice; must be a positive integer. See [PDF documents](#pdf-documents) |
| `DOCUMENT_MAX_SENTENCES` | no (default `1500`) | Cap on how many sentences one document may submit for analysis; must be a positive integer |
| `DOCUMENT_ANALYSIS_TIMEOUT` | no (default `30m`) | Bounds one document analysis run; must be a positive Go duration |

## Analysis cache (instant replay)

A completed imported or uploaded video can replay its analysis instantly instead of re-running the
live pipeline. When `REDIS_URL` points at a reachable Redis or Valkey, the backend caches the full
transcript and verdicts on clean completion under the key `analysis:v1:<video-id>` with an
`ANALYSIS_CACHE_TTL` lifetime (default `24h`); reopening that video before the entry expires loads
everything at once, with no AssemblyAI transcription and no LLM calls.

- **Opt-in, no-op fallback.** The cache is active only when `REDIS_URL` is set and reachable. The
  backend pings it at startup and silently falls back to a no-op cache when `REDIS_URL` is empty or
  the server is unreachable, so a missing or down cache never blocks boot and behaviour is identical
  to having no cache. `make up` runs a local `redis` service (`redis:8-alpine`) and Compose wires
  `REDIS_URL` to it automatically, so the cache is on by default in local dev.
- **What is cached.** Only a clean, fully drained completion of an imported or uploaded video is
  persisted. Live streams and partial or aborted sessions cache nothing.
- **On a miss or expiry.** A miss, an expired entry, a disabled cache, or an unreachable server all
  fall through to the normal live pipeline unchanged.

In production the cache is backed by ElastiCache Valkey; see
[Infrastructure -> Analysis cache](infrastructure.md#analysis-cache).

## PDF documents

An admin uploads a PDF from the [backoffice](backoffice.md) to fact-check it once through the same
pipeline as live streams; any authenticated user then reads it on the Documents surface. See
[PDF fact-check](pdf-fact-check.md) for the full flow and access model. Three backend caps bound LLM
cost and abuse. All keep the defaults when unset:

| Variable | Default | Purpose |
|----------|---------|---------|
| `DOCUMENT_MAX_SIZE_BYTES` | `31457280` (30 MB) | Rejects an upload larger than this. Positive integer |
| `DOCUMENT_MAX_SENTENCES` | `1500` | Rejects an extraction with more sentences than this. Positive integer |
| `DOCUMENT_ANALYSIS_TIMEOUT` | `30m` | Bounds one whole analysis run; most sentences are gated out, so real runs are far shorter. Positive Go duration |

**Analysis depends on the verify path.** The document analyzer reuses the retrieve-then-verify
pipeline, so analysis runs only when the verify path is configured (`FACTCHECK_VERIFY_PATH` active
with its Anthropic key). With the verify path off, upload, extraction, listing, and viewing still
work, but analysis is disabled: extraction stores the sentences and flips the document `ready` while
leaving `analysis_status = none`, and a reanalyse request returns a clear "analysis is disabled"
error.

**Compose forwards the verify-path variables.** A variable set in `.env` does nothing unless
`docker-compose.yml` passes it through. The backend service forwards the document caps and the whole
`FACTCHECK_VERIFY_*` (and `FACTCHECK_POLITICAL`) set, so the analyzer's behaviour in local dev is
governed by those forwarded verify-path variables. To exercise document analysis locally, set
`FACTCHECK_VERIFY_PATH=true` and `FACTCHECK_VERIFY_API_KEY` in `.env` before `make up`.

## TV capture

The `tvcapture` worker records enabled TV channels, feeds their audio into the live pipeline, and
optionally archives them to S3. It is a separate long-running worker (`cmd/tvcapture`) gated by
`TV_CAPTURE_ENABLED`; when off it idles without requiring credentials. For the full operator flow,
legal posture, and provisioning, see the [Live TV capture runbook](tv-live.md). Defaults are the ones
`config.LoadTVCapture` applies.

| Variable | Default | Purpose |
|----------|---------|---------|
| `TV_CAPTURE_ENABLED` | `false` | Master switch for the worker. Off = the worker idles (no capture, no credential required) |
| `TV_CAPTURE_BACKEND_URL` | `http://localhost:8080` | Backend API base the worker calls (channel list, presign, register, prune) and dials the feed WebSocket on. Operator-provided in the cloud (internal ALB or public URL the host can reach) |
| `TV_CAPTURE_TOKEN_URL` | derived from `KEYCLOAK_ISSUER` (`<issuer>/protocol/openid-connect/token`) | Keycloak token endpoint for the client-credentials grant. Override only for an unusual topology |
| `TV_CAPTURE_CLIENT_ID` | `tv-capture` | Keycloak service-account client the worker authenticates as; carries the scoped `tv-capture` realm role (not admin) |
| `TV_CAPTURE_CLIENT_SECRET` | (none) | The service-account client secret. **Required when `TV_CAPTURE_ENABLED=true`**; read from env only, never logged. In the cloud it comes from Secrets Manager |
| `TV_SEGMENT_SECONDS` | `3600` | Archive segment length in seconds (hourly MPEG-TS segments). Range 60–86400 |
| `TV_RECORDING_RETENTION_DAYS` | `30` | Days a `tv` recording is kept before the daily prune deletes its S3 object and row. Range 1–3650. This app-level prune is authoritative; the S3 lifecycle backstop is off by default |
| `TV_FEED_STALL_SECONDS` | `60` | How long a feed may go silent before the worker treats it as stalled and restarts the pipeline. Range 5–3600 |
| `TV_CAPTURE_POLL_SECONDS` | `30` | Reconcile interval: how often the worker re-reads the enabled channels and starts/stops pipelines. Range 5–3600 |
| `TV_CAPTURE_WORK_DIR` | `/work` | Where in-progress segments are written before upload; a persistent volume mounts here so a crash leaves partial segments for the startup salvage pass |
| `KEYCLOAK_ADDITIONAL_CLIENT_IDS` | (none) | Comma-separated extra authorized parties (`azp`) the backend verifier accepts beyond the web client. Must include `tv-capture` wherever the backend validates the worker's client-credentials token |

## Ingestion fleet configuration

These knobs govern the ingestion pipeline (broker, workers, category crawl, scheduler). Most are
optional and read only by the ingestion producers/workers, not the live API. Defaults below are the
ones the config loaders in `stack/backend/internal/config` apply; the full mechanics are in
[the ingestion pipeline](ingestion-pipeline.md). Every source's per-connector knobs (parliament,
SDMX, ODS, DataCommons, Legifrance, ...) are documented in
[the source inventory](fact-check-sources.md).

### Broker and queue resilience

| Variable | Default | Effect |
|----------|---------|--------|
| `RABBITMQ_URL` | (unset) | AMQP broker connection string; carries credentials, never logged. Compose wires the local `rabbitmq` service |
| `RABBITMQ_QUEUE` | `embedding.jobs` | Base name of the embedding-job queue |
| `RABBITMQ_QUEUE_VERSIONS` | `2` | Comma-separated, oldest-first version list; the queue is `<base>.v<version>` and the newest is active. Append a version to roll |
| `RABBITMQ_MAX_PRIORITY` | `10` | `x-max-priority` ceiling (1-255); higher-priority units delivered first |
| `RABBITMQ_PREFETCH` | `1` | Unacked messages the broker pushes to one consumer (0 = unbounded); the embed worker sizes it to in-flight batches |
| `RABBITMQ_DLQ_ENABLED` | `true` | Route a rejected message to the companion `<base>.dlq.v<n>` dead-letter queue instead of discarding it. Must be identical on producers and consumers |
| `RABBITMQ_RECONNECT_MIN_BACKOFF` | `250ms` | First redial wait after a broker drop |
| `RABBITMQ_RECONNECT_MAX_BACKOFF` | `30s` | Redial-wait ceiling; must be >= the min. Each redial doubles up to this, jittered to half |

### Embedding worker

| Variable | Default | Effect |
|----------|---------|--------|
| `EMBEDWORKER_REPLICAS` | `2` | Competing embed workers (a `make` argument, not Compose); linear throughput |
| `EMBED_WORKER_CONCURRENCY` | `4` | In-flight batches per replica |
| `EMBED_WORKER_BATCH_SIZE` | `128` | Chunks per Voyage call (<= 1000); the main throughput lever |
| `EMBED_WORKER_MAX_BATCH_TOKENS` | `96000` | Token budget per Voyage call (80% of the 120000 ceiling); an over-budget batch is split before the call |
| `EMBED_WORKER_BATCH_WAIT` | `200ms` | How long a partial batch waits before sending |
| `EMBED_WORKER_MAX_ATTEMPTS` | `5` | Delivery budget before a job is dead-lettered |
| `EMBED_WORKER_RPM` | `0` (unpaced) | Optional per-replica Voyage rate cap |
| `WORKER_IDLE_TIMEOUT` | `0` (off) | Drain-to-idle window shared by every queue worker: a worker whose queue is empty this long exits cleanly (the consumer host's `--stop-when-idle` self-stop keys on it). Off locally; capped at 24h |

### Category crawl (`wikicrawl`)

| Variable | Default | Effect |
|----------|---------|--------|
| `CRAWL_CATEGORIES` | (required) | Comma-separated category titles, e.g. `Category:Physics` |
| `CRAWL_PROJECT` | `WIKI_CORPUS` | Wiki project queried and used to build article URLs |
| `CRAWL_CORPUS` | `<project>-crawl` | Provenance tag stored in `evidence_chunks.source` |
| `CRAWL_MAX_DEPTH` | `1` | Subcategory recursion depth (0 = direct pages only) |
| `CRAWL_MAX_PAGES` | `5000` | Hard cap on distinct pages collected |
| `CRAWL_INCLUDE_BODY` | `true` | When false, ingest lead only |
| `CRAWL_CHECKPOINT_PATH` | `/state/crawl-checkpoint.json` | Resume state file; a per-shard suffix isolates parallel shards |
| `CRAWL_ERROR_BUDGET` | `50` | Pages a run may skip (extract or fail-closed gate error) before it aborts |
| `CRAWL_CHECKWORTHY` | `true` | Producer-side fact-checkability gate; `false` publishes every chunk |
| `CHECKWORTHY_API_KEY` | (required when gate on) | Anthropic key for the gate; never logged |
| `CRAWL_CHECKWORTHY_MODEL` | `claude-haiku-4-5-20251001` | Gate model |
| `CRAWL_CHECKWORTHY_CONCURRENCY` | `8` | In-flight gate judgments in the producer |
| `CRAWL_CHECKWORTHY_RPM` | `0` (unpaced) | Per-producer gate call-rate cap |
| `CRAWLWORKER_REPLICAS` | `2` | Competing crawl-worker replicas |

### Scheduler

The always-on `scheduler` service fires each enabled producer on its cron. Every source defaults
disabled. The three originally-scheduled sources have dedicated knobs; every other registry source is
enabled by `SCHEDULE_<SOURCE>_ENABLED=true` and takes its `DefaultCron` from the registry (overridable
with `SCHEDULE_<SOURCE>_CRON`).

| Variable | Default | Effect |
|----------|---------|--------|
| `SCHEDULE_WIKIPEDIA_ENABLED` / `_CRON` | `false` / `0 3 * * *` | Wikipedia category crawl |
| `SCHEDULE_FACTCHECK_ENABLED` / `_CRON` | `false` / `0 4 * * *` | Fact-check-archive crawl |
| `SCHEDULE_SCRUTINS_ENABLED` / `_CRON` | `false` / `30 4 * * *` | Scrutins crawl |
| `SCHEDULE_JITTER` | `30s` | Random per-run delay spreading concurrent sources (capped at 1h) |

## Retrieval and matching

Query-time evidence retrieval and scoring. All are optional (empty keeps the default); change a
default only with golden-eval (`make eval`) and latency evidence. Verified against
`stack/backend/internal/config/config.go`.

### Hybrid search and evidence index (VER-195, VER-176, VER-203)

| Variable | Default | Effect |
|----------|---------|--------|
| `MATCH_HYBRID_SEARCH` | `true` | Fuse a French lexical full-text search with the vector search by RRF; `false` forces pure vector search |
| `MATCH_LEXICAL_TOP_K` | `20` | Lexical candidate pool per corpus |
| `MATCH_RRF_K` | `60` | RRF smoothing constant |
| `EVIDENCE_BQ_MULTIPLIER` | `0` (off) | Positive value enables the two-stage binary-quantization evidence search (coarse bit index + halfvec rerank). No effect while hybrid search is on (single-stage vector branch) |
| `EVIDENCE_BQ_THRESHOLD_VECTORS` | `50000000` | Embedded-evidence count beyond which `make pipeline-health` warns to enable BQ. Drives only the health warning, not BQ itself |
| `EVIDENCE_NEAR_DUP_SIMILARITY` | `0` (off) | Cosine bar at embed-write time: a fresh chunk at/above its nearest same-source neighbour is stored for provenance but withheld from search. A sensible on value is ~0.97 |

### Matcher tuning (VER-202)

| Variable | Default | Effect |
|----------|---------|--------|
| `MATCH_TOP_K` | `5` | Curated-claim neighbours retrieved |
| `MATCH_SCORE_THRESHOLD` | `0.5` | Curated-claim cosine floor |
| `MATCH_EVIDENCE_TOP_K` | `5` | Evidence neighbours; 0 disables evidence |
| `MATCH_EVIDENCE_SCORE_THRESHOLD` | `0.6` | Evidence cosine floor |
| `MATCH_MAX_RESULTS` | `5` | Merged results kept across both corpora |
| `MATCH_EMBED_CONCURRENCY` | `4` | In-flight query-embed calls |
| `MATCH_TIMEOUT` | `10s` | One segment's whole match budget |
| `MATCH_CONFIDENCE_CLUSTER_SIZE` | `5` | Matches aggregated into the confidence score |
| `MATCH_CONFIDENCE_LEAD_WEIGHT` / `_BODY_WEIGHT` | `1.0` / `0.6` | Lead / body chunk corroboration weight |
| `MATCH_CLAIMS_EF_SEARCH` / `MATCH_EVIDENCE_EF_SEARCH` | `0` (session default) | Per-corpus HNSW `ef_search` |

## LLM provider and the fact-check verify path

The LLM stages (check-worthiness gate, claim decomposition, credibility/political verifier, ingest
gate) share one provider; the terminal reasoning gate is decoupled. Keys are secrets, never logged.

| Variable | Default | Effect |
|----------|---------|--------|
| `LLM_PROVIDER` | `deepseek` | `deepseek` (cheap chat model over the OpenAI-compatible API), `anthropic` (Claude Haiku), or `gemini`. An unknown value fails fast at startup |
| `DEEPSEEK_API_KEY` | (required when provider is deepseek) | DeepSeek key |
| `GEMINI_API_KEY` | (required when `LLM_PROVIDER=gemini`) | Google Gemini key |

### Retrieve-then-verify path (off by default)

| Variable | Default | Effect |
|----------|---------|--------|
| `FACTCHECK_VERIFY_PATH` | `false` | Decompose each unit into atomic claims, retrieve evidence, and have an LLM verifier derive the verdict. Read at startup |
| `FACTCHECK_VERIFY_API_KEY` | (Anthropic key when provider is anthropic) | Verifier key; with none it stays on the legacy path |
| `FACTCHECK_VERIFY_MODEL` | `claude-haiku-4-5-20251001` | Decomposer + verifier model |
| `FACTCHECK_VERIFY_MAX_CLAIMS_PER_UNIT` | `4` | Cap on atomic claims per unit |
| `FACTCHECK_VERIFY_FAST_TAU` | `0.85` | Curated near-match borrow threshold |
| `FACTCHECK_VERIFY_RETRIEVAL_THRESHOLD` | `0.45` | Recall floor for evidence fed to the verifier (below the legacy 0.6 borrow floor on purpose) |
| `FACTCHECK_VERIFY_CONCURRENCY` / `_QUEUE_DEPTH` | `2` / `4` | In-flight verify calls / queued claims |
| `FACTCHECK_VERIFY_FAST_DEADLINE` / `_DEADLINE` | `800ms` / `4s` | Decompose+retrieve bound / one verify call bound |
| `FACTCHECK_VERIFY_CACHE_TTL` | `30s` | Semantic-claim-cache window (0 disables); a paraphrase above the threshold replays the cached verdict |
| `FACTCHECK_VERIFY_CACHE_THRESHOLD` | `0.95` | Cosine bar for a cache hit |
| `FACTCHECK_VERIFY_CACHE_MAX_ENTRIES` | `1024` | In-process cache size, oldest evicted first |
| `FACTCHECK_KNOWLEDGE_FALLBACK` | `true` | A claim that retrieves no evidence is still judged by the verifier from general knowledge (basis `knowledge`, confidence capped) instead of short-circuiting to a blank unverifiable; under pool saturation or a verifier failure it degrades back to that instant unverifiable, never to unchecked. `false` restores the strict short-circuit everywhere, including the terminal gate's knowledge floor |
| `LIVE_MAX_SENTENCES` | `4` | Sentences accumulated into one live analysis unit before it is scored (the decomposer's window); the previous unit's trailing sentence is always passed as decomposition context |

### Terminal reasoning gate (VER-192, off by default)

When the verify path's best verdict is weak (unverifiable, or confidence below the trigger floor), a
stronger reasoner re-judges the **same** retrieved evidence once, off the live hot path, and upgrades
the verdict in place only when its re-judgment is grounded **and** reaches the min-confidence floor.
Its provider is decoupled from `LLM_PROVIDER`. Every knob falls back to its `FACTCHECK_SECOND_PASS_*`
equivalent.

| Variable | Default | Effect |
|----------|---------|--------|
| `FACTCHECK_SECOND_PASS` | `false` | Turn the reasoner on (`FACTCHECK_FINAL_GATE=true` also enables it) |
| `FACTCHECK_FINAL_GATE_PROVIDER` | follows `LLM_PROVIDER` | Decoupled provider override for the reasoner |
| `FACTCHECK_FINAL_GATE_API_KEY` | `FACTCHECK_SECOND_PASS_API_KEY` | Anthropic key override |
| `FACTCHECK_FINAL_GATE_MODEL` | the second pass's model | Reasoning model |
| `FACTCHECK_FINAL_GATE_TRIGGER_BELOW` | `0.8` | Escalate a verdict below this confidence |
| `FACTCHECK_FINAL_GATE_MIN_CONFIDENCE` | `0.90` | Grounded confidence required to adopt the re-judgment |
| `FACTCHECK_FINAL_GATE_DEADLINE` | `12s` | One reverify call bound |
| `FACTCHECK_FINAL_GATE_KNOWLEDGE_FLOOR` | `0.5` | Sparse-corpus loosening: an indeterminate or sub-floor no-passage verdict escalates too, and a knowledge-basis re-judgment settling a definite verdict at/above this floor is adopted - only for claims with no passages at all, so an uncited opinion can never displace an evidence-grounded verdict. Inert when `FACTCHECK_KNOWLEDGE_FALLBACK=false`; `0` restores the strict evidence-only gate |

The French/EU political two-axis mode (`FACTCHECK_POLITICAL`) and its source packs
(`WEBSEARCH_API_KEY`, `PRESS_API_KEY`, stats-pack tuning) are documented inline in `.env.example`;
they are read only when political mode is on.

## Authentication (Keycloak identity gate)

Signing in through Keycloak is sufficient end to end: the whole `/api` subtree (every data flow and
the live WebSocket) is gated on the verified Keycloak identity, and the live admin-debug detail is
gated on a verified `admin` claim. There is no separate backend login to maintain.

- **`/api` HTTP requests.** The frontend promotes the httpOnly Keycloak access token to an
  `Authorization: Bearer` header at the proxy boundary; the backend's identity gate validates it and
  attaches the verified role. A request with no token, or an invalid/expired/wrong-issuer token, is
  rejected with `401`. A valid token reaches the route; the `admin`-only debug endpoints then require
  a verified `admin` claim (`403` otherwise).
- **The live WebSocket.** A browser cannot set the `Authorization` header on a WebSocket handshake,
  so the token rides the `access_token` query parameter (the name [RFC 6750
  §2.3](https://www.rfc-editor.org/rfc/rfc6750.html#section-2.3) defines for exactly this case). The
  backend validates it the same way; the per-passage evidence detail is emitted only when
  `DEBUG_FACT_CHECK` is on **and** the socket carries a verified `admin` claim. Server-side
  enforcement is authoritative — nothing a client sends can flip the detail on. The access log records
  only the request path, never the query string, so the token is not persisted. Use short-lived
  tokens and `wss://` in production.

### Legacy password login (retired, opt-in)

The original single-operator password-session login (`/api/login` + `/api/logout`, an argon2id hash
and an HMAC session cookie) has been **retired**: by default it no longer gates `/api`, and the
`session` cookie is no longer accepted there — a verified Keycloak token is the only credential. It is
**not deleted** — set `LEGACY_PASSWORD_LOGIN=true` to restore it for an environment that has no
Keycloak yet. With the flag on, the login/logout routes are registered and the `/api` gate widens to
admit **either** a valid session cookie **or** a Keycloak token; the `admin` role still rides a
verified Keycloak claim only, so a session-cookie caller is always a guest. When off (the default,
including dev and production), the password machinery is never wired and `AUTH_EMAIL` /
`AUTH_PASSWORD_HASH` / `SESSION_SECRET` are not read. When you opt back in, generate the secrets by
hand:

```bash
cd stack/backend
printf '%s' "your-password" | go run ./cmd/genhash   # -> AUTH_PASSWORD_HASH (single-quote it in .env)
openssl rand -hex 32                                  # -> SESSION_SECRET
```

Legacy sessions are stateless HMAC tokens: to revoke every outstanding one, rotate `SESSION_SECRET`
and restart the backend.

## Local Keycloak (identity provider)

`make up` (or `make keycloak` on its own) starts a local Keycloak on `:8081` that imports
`stack/keycloak/realm.json` on startup. It is the local mirror of the production identity provider,
which is self-hosted by Terraform (module `keycloak`) at `https://login.jeminforme.fr`; only the realm
definition is shared between the two, so the backend and frontend issuer config lines up across
environments.

| Property | Local value | Production (self-hosted on ECS) |
|----------|-------------|---------------------------------|
| Issuer | `http://localhost:8081/realms/truth-in-stream` | `https://login.jeminforme.fr/realms/truth-in-stream` |
| Realm | `truth-in-stream` | `truth-in-stream` (same `realm.json` shape) |
| OIDC client | `truth-in-stream-web` (public, PKCE S256, standard flow) | same client id |
| Realm roles | `admin`, `guest` | `admin`, `guest` |
| Default role | `guest` (granted to every new user) | `guest` |
| Roles claim | `realm_access.roles` in the access token | `realm_access.roles` |
| Admin console | `http://localhost:8081` (`admin` / `test1234`, dev only) | `https://login.jeminforme.fr/admin`; bootstrap admin from the `keycloak/bootstrap-admin-password` secret (generated by Terraform) |

The realm ships two dev users for local login and token tests: `admin` / `test1234` (realm roles
`admin` + `guest`) and `guest` / `guest` (role `guest`). These credentials, the bootstrap admin, and
the public client are **local-dev only** — `realm.json` holds no real secret.

The realm definition is the shared contract (realm name, roles, default role, client id, and the
`realm_access.roles` claim), not a verbatim production export. Production ships its own hardened realm
(`stack/keycloak/realm-prod.json`, baked into the optimized Keycloak image and imported on first
boot): the same shape, but with `sslRequired=none` (TLS terminates at CloudFront; the internal ALB
hop is HTTP), Postgres-backed storage on the dedicated RDS `keycloak` database instead of the dev
profile's ephemeral H2, the `https://jeminforme.fr` redirect URIs and web origins, no dev users
(real operators are created in the admin console), and the direct-access (password) grant turned
**off** so production authentication is browser + PKCE only. Keep the issuer, realm, client id,
roles, and roles-claim in the table above identical across environments so the backend and frontend
validate the same tokens; the transport, storage, and grant hardening are production-side concerns.

In production the app services set `KEYCLOAK_ISSUER=https://login.jeminforme.fr/realms/truth-in-stream`
(the single public issuer for both browser and back-channel) and the frontend also sets
`NEXT_PUBLIC_APP_URL=https://jeminforme.fr`. Terraform generates both the Keycloak bootstrap admin
password and the scoped `keycloak` DB role password and stores them in Secrets Manager, so first-boot
admin access is never blocked on a manual push; read the generated bootstrap password with `aws
secretsmanager get-secret-value --secret-id truth-in-stream/prod/keycloak/bootstrap-admin-password
--region eu-west-3`. Standing prod up is a deliberate `terraform apply` of `stack/terraform/prod`; pushing a
`v*` tag whose commit is on `main` then applies prod (behind the `production` Environment approval)
and rolls the services (including Keycloak's DB bootstrap). See
[Infrastructure -> Deploys](infrastructure.md#deploys-human-gated) and the
[prod Keycloak setup runbook](keycloak-prod-setup.md).

Backend and frontend cards point their OIDC issuer at the table's issuer URL and validate the
`realm_access.roles` claim to gate `admin`-only routes; `guest` is the baseline every authenticated
user carries.

The frontend signs in through Keycloak with a server-side authorization-code + PKCE flow (the
`/auth/login`, `/auth/callback`, `/auth/refresh`, and `/auth/logout` route handlers). The access,
refresh, and id tokens are kept in httpOnly cookies, never reaching client JavaScript; the access
token is promoted to an `Authorization: Bearer` header for `/api` requests at the proxy boundary, and
the caller's role is read from the verified token server-side to reveal the `admin`-only debug toggle.
The frontend reads `KEYCLOAK_ISSUER` and `KEYCLOAK_CLIENT_ID` (defaulting to the local realm) plus
`NEXT_PUBLIC_APP_URL` for the redirect/post-logout URIs; over plain-HTTP local dev the OIDC client
relaxes its HTTPS-only requirement for an `http://` issuer only.

### Two-face networking in the docker-compose stack

A single issuer URL cannot be reached identically by the browser and by the backend/frontend
containers in docker-compose, and that is the whole reason local sign-in needs a deliberate split.
The issuer is `http://localhost:8081/...`, but `localhost:8081` inside a container is that container's
own loopback, so a server-side call (the backend's JWKS refresh, the frontend's OIDC discovery, the
token exchange) to `localhost:8081` connection-refuses. In production the issuer is a publicly
routable HTTPS host (`https://login.jeminforme.fr/...`) reachable by every party, so the split does
not exist there.

The dev stack resolves this with **Keycloak hostname v2**, configured on the `keycloak` service in
`docker-compose.yml` — the same mechanism any reverse-proxied production deployment uses:

| Env (Keycloak service) | Value | Effect |
|------------------------|-------|--------|
| `KC_HOSTNAME` | `http://localhost:8081` | Pins the **browser face** — the issuer claim and the authorize/logout URLs in the discovery document — to `localhost:8081`, regardless of which host the request arrived on. |
| `KC_HOSTNAME_BACKCHANNEL_DYNAMIC` | `true` | Resolves the **back-channel face** — token, certs/JWKS, userinfo — from the request host, so a container calling over `keycloak:8081` gets `keycloak:8081` endpoints it can actually reach. |

One discovery document therefore serves both faces: browser endpoints on `localhost:8081`,
back-channel endpoints on `keycloak:8081`. Three dev-only env values wire the services to it, and
**all three are unset in production**, where the single public issuer needs none of them:

| Env | Service | Local value | Production |
|-----|---------|-------------|------------|
| `KEYCLOAK_ISSUER` | backend + frontend | `http://localhost:8081/realms/truth-in-stream` (the public face; validates the token's issuer claim and is where the browser is redirected) | the public HTTPS issuer |
| `KEYCLOAK_JWKS_URL` | backend | `http://keycloak:8081/realms/truth-in-stream/protocol/openid-connect/certs` (the container-reachable back-channel JWKS host) | unset — defaults to the issuer's certs endpoint |
| `KEYCLOAK_INTERNAL_URL` | frontend | `http://keycloak:8081/realms/truth-in-stream` (the host the frontend container runs OIDC discovery and the back-channel code/refresh exchanges against) | unset — defaults to the issuer |

The frontend runs OIDC discovery against `KEYCLOAK_INTERNAL_URL` (the host it can reach) but
validates the returned document against the public `KEYCLOAK_ISSUER`. This is safe because the
discovery processor checks only the document's `issuer` claim, not the URL it was fetched from; with
the back-channel-dynamic Keycloak, that document carries the public browser endpoints and the
internal token/JWKS endpoints, so the browser redirect always lands on `localhost:8081` while the
server-side calls stay on `keycloak:8081`. When `KEYCLOAK_INTERNAL_URL` is unset, `internalUrl` equals
the issuer and discovery is ordinary single-host behaviour — production is unchanged.

> Do **not** point `KEYCLOAK_ISSUER` at `keycloak:8081`: it would corrupt the issuer claim every token
> carries and send the browser to a host it cannot resolve. The public issuer is the browser's face;
> only the back-channel overrides ever name `keycloak:8081`. No hosts-file entry or custom DNS is
> needed — that is the point of the two-face contract.

A build-tagged Go smoke test (`stack/backend/internal/keycloaksmoke`, behind the `keycloak_smoke`
tag) guards this contract end to end against the booted stack: it does an `admin` password grant at
the token endpoint, calls a representative `/api` route with the resulting bearer (expecting it
accepted, not 401), and asserts `GET /auth/login` returns a 307 whose `Location` host is
`localhost:8081`. It runs only in its own CI job (`keycloak-smoke` in `pr.yml`), never in the normal
`go test ./...` run, so the whole login chain cannot silently break again.

## Local development data

`make up` seeds a realistic, fully offline dataset (curated claims, a Wikipedia evidence subset, demo
results) from a committed embedding cache. Manage it with one-command targets:

| Command | What it does |
|---------|--------------|
| `make reset` | Soft reset: drop the schema, re-migrate, reseed (seconds; container stays up) |
| `make reset-hard` | Discard the Postgres volume and rebuild from scratch |
| `make seed` | Reseed every dataset; idempotent |
| `make seed-claims` / `make seed-wiki` / `make seed-videos` | Seed one dataset for targeted testing |
| `make refresh-embeddings` | Regenerate the committed embedding cache from fixtures via Voyage (needs `EMBEDDING_API_KEY`) |

The shipped cache holds deterministic placeholder vectors, so a full reseed is offline and
deterministic; `make refresh-embeddings` swaps in real `voyage-4-large` vectors once you have a key.
A soft `make reset` rebuilds the schema without restarting the backend, so the sample-video gallery
(upserted on startup by `VideoService.EnsureSamples`) is empty until `docker compose restart backend`
or a `make reset-hard`.
