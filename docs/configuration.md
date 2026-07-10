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
| `DOCUMENT_MAX_SIZE_BYTES` | no (default 30 MB) | Upload-size cap for a PDF on the Documents surface; must be a positive integer. See [PDF documents](#pdf-documents) |
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

The Documents surface lets an admin upload a PDF and fact-check it once through the same pipeline as
live streams; see [PDF fact-check](pdf-fact-check.md) for the full flow and access model. Three
backend caps bound LLM cost and abuse. All keep the defaults when unset:

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
