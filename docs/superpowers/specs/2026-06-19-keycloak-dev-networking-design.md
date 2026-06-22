# Proper local Keycloak setup (dev networking contract)

Date: 2026-06-19
Status: Approved (design)

## Problem

Local sign-in through Keycloak was broken in the docker-compose stack: the backend logged
`keycloak jwks refresh failed` and the frontend returned `GET /auth/login 500`, both with
`ECONNREFUSED 127.0.0.1:8081`.

Root cause is in the original setup, not a one-off bug. `docs/configuration.md` defines a single
issuer, `http://localhost:8081/realms/truth-in-stream`, and states that "the backend and frontend
issuer config lines up across environments." That assumption is wrong for docker-compose dev:

- The issuer doubles as the host the server-side code calls (backend JWKS refresh; frontend OIDC
  discovery and the authorization-code / refresh token exchanges).
- `localhost:8081` inside a container is that container's own loopback, where nothing listens, so
  every server-side call was guaranteed to fail.
- In production the same single issuer works because it is a publicly routable HTTPS hostname
  (`https://login.jeminforme.fr/...`), reached identically by the browser and by every service.

`stack/keycloak/realm.json` is correct and in spec: it seeds the `truth-in-stream-web` public PKCE
client, the `admin`/`guest` roles, the `guest` default role, and two dev users (`admin`/`admin`,
`guest`/`guest`). User management needs no work. The gap is purely that the **dev networking was
never designed**, and when it broke the fix was applied reactively with no test guarding it - so it
could regress silently again.

A public issuer URL that also resolves from inside containers is impossible in docker-compose
without a manual `/etc/hosts` entry or custom DNS (both rejected: they are per-machine, easy to
forget, and not reproducible). `localhost` is intrinsically host-local. Therefore a browser face and
a back-channel face are unavoidable in dev; the goal is to make that split deliberate, documented,
and test-guarded rather than a surprise.

## Goals

- Local Keycloak sign-in works out of the box after `make up`, with no manual host edits.
- The browser-vs-container URL split is a deliberate, documented contract using Keycloak's own
  hostname mechanism - the same one a reverse-proxied production deployment uses.
- A CI regression guard proves the full login chain works, so this cannot silently break again.
- Production behaviour is unchanged: with the dev-only env unset, there is a single public issuer.

## Non-goals

- No `realm.json` changes (roles, client, users, default role stay as-is).
- No new login UI; no change to the retired password-login path.
- No production or terraform changes.
- No fail-fast startup reachability check (considered, deferred).
- No new dedicated `make` smoke target (the guard lives in CI).

## Design

### 1. Dev networking contract (Keycloak hostname v2)

Keycloak serves two URL faces from one realm:

- **Browser face** - `KC_HOSTNAME=http://localhost:8081`. Pins the issuer and the browser-facing
  endpoints (`authorize`, `logout`) to `localhost:8081` regardless of which internal host the
  request arrived on. This is what the host browser reaches over the published port and what tokens
  carry as `iss`.
- **Back-channel face** - `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`. The back-channel endpoints
  (`token`, `certs`/JWKS, `userinfo`) are resolved from the request host, so a service calling from
  inside the compose network over `keycloak:8081` gets `keycloak:8081` endpoints it can reach.

One discovery document therefore serves both: browser endpoints on `localhost:8081`, back-channel
endpoints on `keycloak:8081`. Verified against Keycloak 26 hostname v2
(https://www.keycloak.org/server/hostname).

### 2. Service wiring (three env values, defaulted off in prod)

- **Backend** (`docker-compose.yml`):
  - `KEYCLOAK_ISSUER=http://localhost:8081/realms/truth-in-stream` - validates the token `iss`.
  - `KEYCLOAK_JWKS_URL=http://keycloak:8081/realms/truth-in-stream/protocol/openid-connect/certs` -
    the existing, documented override in `internal/config/config.go`; no Go change needed.
- **Frontend** (`docker-compose.yml` + a config seam):
  - `KEYCLOAK_ISSUER=http://localhost:8081/realms/truth-in-stream` - public identity for the browser
    redirect and issuer validation.
  - `KEYCLOAK_INTERNAL_URL=http://keycloak:8081/realms/truth-in-stream` - a new config field
    (`src/lib/auth/config.ts`) that defaults to the issuer when unset. `authServer()` in
    `src/lib/auth/oidc.ts` runs OIDC discovery against `internalUrl` but validates the response
    against the public issuer; with back-channel-dynamic the returned metadata then carries
    browser-facing endpoints on the public host and `token`/`jwks` on the internal host. Safe
    because oauth4webapi 3.8.6 `processDiscoveryResponse` validates only the document's `issuer`
    claim, not the URL it was fetched from.
- **Keycloak** (`docker-compose.yml`): `KC_HOSTNAME` + `KC_HOSTNAME_BACKCHANNEL_DYNAMIC` as above.

Production sets none of `KEYCLOAK_JWKS_URL`, `KEYCLOAK_INTERNAL_URL`, `KC_HOSTNAME*`: `internalUrl`
defaults to the issuer, the JWKS URL derives from the issuer, and discovery uses the single public
issuer - identical to today.

### 3. Regression guard - headless login smoke test in CI

A new CI job boots the compose stack and runs one build-tagged Go integration test
(`//go:build keycloak_smoke`) that exercises the whole chain without a browser:

1. **Token issuance / back-channel reachability.** Resource-owner password grant of the seeded
   `admin`/`admin` user (the realm enables `directAccessGrantsEnabled` for dev token tests) at the
   internal token endpoint -> receive an access token. Proves Keycloak is up, the realm imported,
   and the back-channel host is reachable.
2. **Backend JWKS validation.** Call a representative `/api` endpoint with that bearer -> assert the
   backend accepts it (not `401`). Proves the backend's JWKS URL resolves and the token validates.
3. **Browser redirect correctness.** `GET http://localhost:3000/auth/login` -> assert `307` with a
   `Location` host of `localhost:8081` (browser-reachable), never `keycloak:8081`. Proves the
   browser-facing redirect uses the public face.

The test runs only in the new CI job against the live stack (the build tag keeps it out of the
ordinary `go test ./...` and Vitest runs).

### 4. Documentation

Update the "Local Keycloak" section of `docs/configuration.md`:

- Replace the single-issuer framing with the two-face contract (public issuer vs internal
  back-channel) and the `KEYCLOAK_INTERNAL_URL` env.
- State explicitly that the dev split exists because `localhost` differs between the host browser and
  the containers, while production's routable issuer needs none of it.
- Keep the existing local/prod parity table (issuer, realm, roles, client, dev users) accurate.

## Testing

- Unit (already in the working tree): `config.test.ts` covers `internalUrl` defaulting and override;
  `oidc.test.ts` covers `authServer()` discovering over the internal URL while validating the public
  issuer. `tsc` and `eslint` clean.
- CI smoke (new): the build-tagged Go integration test above, in a dedicated job that boots the
  stack.

## Acceptance criteria

- After `make up`, signing in through Keycloak works end to end with no manual host file edits.
- Backend logs `keycloak token validation enabled` (issuer `localhost:8081`, JWKS `keycloak:8081`)
  and no `jwks refresh failed`.
- `GET /auth/login` returns `307` to a `localhost:8081` authorize URL.
- The CI smoke job is green and fails if any leg of the login chain breaks.
- `docs/configuration.md` documents the contract; `realm.json` and production config are unchanged.
- Unit tests, `tsc`, `eslint`, and `go test -race ./...` are green.
