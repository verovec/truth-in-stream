# Configuration and local data

This page covers environment configuration, the auth secrets, and the offline seeded dataset that
`make up` brings up. For the one-command quick start, see the [README](../README.md#quick-start).

## Environment variables

Secrets come from a local `.env` (gitignored); `docker compose` interpolates it. The full set of
tuning knobs lives in `stack/backend/internal/config`. The essentials:

| Variable | Required | Purpose |
|----------|----------|---------|
| `DATABASE_URL` | yes (compose sets a dev value) | Postgres + pgvector connection string |
| `TRANSCRIPTION_API_KEY` | yes (live only) | AssemblyAI key. `u3-rt-pro` streaming is the single transcriber for live streams and imported videos. Not used by the offline demo |
| `EMBEDDING_API_KEY` | no for seeding | Voyage `voyage-4-large`. Seeding is offline; needed only for live query embedding, `make refresh-embeddings`, or wiki ingestion |
| `EMBEDDING_MODEL` | no (default `voyage-4-large`) | Must match between ingest and query (different models = different vector spaces); pinned to 1024 dims. Changing it requires `make refresh-embeddings` |
| `AUTH_EMAIL` / `AUTH_PASSWORD_HASH` / `SESSION_SECRET` | yes | Operator login (single user), argon2id hash, and HMAC session key (>= 32 bytes) |
| `SESSION_TTL` | no (default `24h`) | Session lifetime |
| `CORS_ALLOWED_ORIGIN` | no | Leave unset for same-origin dev (session cookie is `SameSite=Strict`) |
| `PORT` | no (default `8080`) | Backend listen port |

## Auth secrets

`make bootstrap` generates the three auth values and is the recommended path. It copies
`.env.example` to `.env` (when absent), fills the three secrets that have no safe default
(`AUTH_EMAIL`, `AUTH_PASSWORD_HASH`, `SESSION_SECRET`), and writes self-describing placeholders for
`TRANSCRIPTION_API_KEY` / `EMBEDDING_API_KEY` so a fresh clone boots and plays the offline demo. It is
idempotent and never writes the plaintext password to disk. Replace the placeholders with real keys
only when you move on to live analysis.

By hand:

```bash
cd stack/backend
printf '%s' "your-password" | go run ./cmd/genhash   # -> AUTH_PASSWORD_HASH (single-quote it in .env)
openssl rand -hex 32                                  # -> SESSION_SECRET
```

Sessions are stateless HMAC tokens: to revoke every outstanding session, rotate `SESSION_SECRET` and
restart the backend.

## Local Keycloak (identity provider)

`make up` (or `make keycloak` on its own) starts a local Keycloak on `:8081` that imports
`stack/keycloak/realm.json` on startup. It is the local mirror of the production identity provider
the operator manages at `https://login.jeminforme.fr`; only the realm definition is shared between
the two, so the backend and frontend issuer config lines up across environments.

| Property | Local value | Production (operator-managed) |
|----------|-------------|-------------------------------|
| Issuer | `http://localhost:8081/realms/truth-in-stream` | `https://login.jeminforme.fr/realms/truth-in-stream` |
| Realm | `truth-in-stream` | `truth-in-stream` (same `realm.json` shape) |
| OIDC client | `truth-in-stream-web` (public, PKCE S256, standard flow) | same client id |
| Realm roles | `admin`, `guest` | `admin`, `guest` |
| Default role | `guest` (granted to every new user) | `guest` |
| Roles claim | `realm_access.roles` in the access token | `realm_access.roles` |
| Admin console | `http://localhost:8081` (`admin` / `admin`, dev only) | operator credentials, out of band |

The realm ships two dev users for local login and token tests: `admin` / `admin` (realm roles
`admin` + `guest`) and `guest` / `guest` (role `guest`). These credentials, the bootstrap admin, and
the public client are **local-dev only** — `realm.json` holds no real secret.

The realm definition is the shared contract (realm name, roles, default role, client id, and the
`realm_access.roles` claim), not a verbatim production export. The operator imports the same shape
into production Keycloak and then hardens it for that environment: TLS and Postgres-backed storage
instead of the dev profile's HTTP + ephemeral H2, real users instead of the two dev accounts, and the
direct-access (password) grant the local client enables for token tests turned **off** so production
authentication is browser + PKCE only. Keep the issuer, realm, client id, roles, and roles-claim in
the table above identical across environments so the backend and frontend validate the same tokens; the
transport, storage, and grant hardening are production-side concerns.

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
