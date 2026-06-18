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
