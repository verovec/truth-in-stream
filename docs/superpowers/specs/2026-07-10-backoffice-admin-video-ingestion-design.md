# Backoffice + admin-only video ingestion (design)

Date: 2026-07-10
Status: approved design; decomposed into cards (see "Card breakdown")

## Problem

Video ingestion is open to every signed-in user. `POST /api/videos/uploads`,
`POST /api/videos/youtube`, and `POST /api/videos/{id}/confirm` sit behind the broad `/api`
identity gate only (`handler.go:31-33`); the `/app` library renders the drag-drop uploader and
the YouTube form unconditionally (`library-experience.tsx:303-304`). Any authenticated `guest`
can upload or import videos.

Admin affordances are also scattered: the PDF uploader is inline in `/documents` (admin-gated),
SRT/CSV exports are inline in `/app`, and debug tooling is a floating button. There is no admin
area; the product has no place where "operator work" lives.

## Goals

- A **backoffice** section of the app, reachable only by Keycloak `admin` users, built on the
  conventions the codebase already has (page-to-shell pattern, server-resolved `role` prop,
  `middleware.RequireAdmin` on the backend).
- **Video upload and YouTube import are possible only from the backoffice.** The backend
  enforces admin on every video ingestion mutation; the `/app` library becomes a pure
  consumption surface (gallery, playback, live analysis).
- Backoffice video management gains parity with documents: a list with status and an
  admin-only delete.
- Keycloak needs **no new automation**: the `admin` realm role already exists in both realm
  files; the local dev realm already seeds an admin user; prod role assignment stays the
  documented manual admin-console step.

## Non-goals

- No separate admin application, subdomain, or second OIDC client. The backoffice is a route
  section of the existing Next.js app riding the existing session plumbing.
- No new Keycloak roles or finer-grained permissions. The single `admin` realm role stays the
  authorization model (`realm_access.roles` contains `"admin"`).
- No change to playback, live analysis, exports, or the documents *contextual* admin actions
  (reanalyse and delete stay on the document viewer page — they act on the thing in front of
  you; the backoffice owns ingestion, not every admin verb).
- No server-side queue or analysis change; the client-driven live analysis model is untouched.

## Access model (reused, not invented)

The repo already has a complete two-tier admin gate; the backoffice adopts it verbatim:

- **Backend (authoritative)**: wrap routes in `middleware.RequireAdmin` (`identity.go:81-89`),
  which 403s unless the verified Keycloak token carries realm role `admin`
  (`auth.go:38,68-96`). This is exactly how document uploads and video exports are gated today
  (`handler.go:39-45,53-54`).
- **Frontend (reveal-only)**: pages resolve `getSession()` server-side and act on
  `session.role`; a forged cookie can only reveal UI, never unlock behaviour. The backoffice
  page goes one step further than component-level hiding: non-admins are **redirected** server
  side, so they never receive the backoffice tree at all.
- **proxy.ts**: `/backoffice` is not added to `PUBLIC_PATHS`, so the existing optimistic
  session gate already bounces cookie-less visitors to `/login`. No proxy change.
- **Legacy password login** (`LEGACY_PASSWORD_LOGIN=true`): `SessionOrIdentity` attaches
  session-cookie callers as guest, never admin (`middleware/auth.go`), so the backoffice and
  its endpoints stay closed even with the retired login enabled.

## Keycloak

- **Local dev — already conformant.** `stack/keycloak/realm.json` defines realm roles `admin`
  and `guest` (default `guest`) and seeds `admin`/`test1234` with
  `realmRoles: ["admin", "guest"]` plus a `guest`/`guest` user. The default local user is
  admin today; no realm change ships with this epic. The `guest` user is kept deliberately —
  it exercises the deny path in e2e.
- **Prod — follows the existing convention.** `stack/keycloak/realm-prod.json` already defines
  the `admin` role and ships `users: []`; operators create users and assign the `admin` role
  in the admin console per `docs/keycloak-prod-setup.md`. That runbook gains one line: the
  `admin` role now also grants backoffice access. No realm import, terraform, or pipeline
  change.

## Design

Principle: **content ingestion is a backoffice operation; consumption stays for every
authenticated user; contextual admin actions remain in context.**

### Backend surface (after the epic)

| Route | Gate |
|---|---|
| `POST /api/videos/uploads` | RequireAdmin (was identity-only) |
| `POST /api/videos/youtube` | RequireAdmin (was identity-only) |
| `POST /api/videos/{id}/confirm` | RequireAdmin (was identity-only) |
| `DELETE /api/videos/{id}` | RequireAdmin (new) |
| `GET /api/videos`, `GET /api/videos/{id}`, `GET /api/videos/{id}/live` | identity (unchanged) |
| `GET /api/videos/{id}/export/*` | RequireAdmin (unchanged) |
| documents routes | unchanged (uploads/extraction/reanalyse/delete already admin) |

Delete follows the documents pattern: remove the media object best-effort, delete the record;
the Redis analysis snapshot for an imported video expires by TTL and needs no explicit
invalidation. Sample videos are deletable like any record — `make seed` re-upserts them
(idempotent by object key), so deletion is recoverable and needs no special casing.

### Frontend surface

- **New route `/backoffice`** following the page-to-shell pattern: a thin async
  `page.tsx` resolves `getSession()` + locale concurrently and **redirects non-admins to
  `/app`**; a synchronous `BackofficeShell` supplies the shared `AppHeader` chrome; a client
  `BackofficeExperience` composes two isolated sections:
  - **Videos**: the existing `VideoUploader` and `YoutubeUrlForm` components move here
    (reused verbatim, along with `use-video-uploads` and `lib/video/api`), plus a management
    list of all videos (status badge, delete control wired to the new endpoint).
  - **Documents**: the existing `DocumentUploader` moves here (with `use-document-uploads`),
    giving PDF ingestion the same home. `/documents` becomes consumption-only.
- **Navigation**: `AppSection` gains `"backoffice"`; the `NAV` entry renders only for admins,
  so `AppHeader` gains a `role` prop (every shell already holds `role`). Dictionary keys land
  in both `fr.ts` and `en.ts`.
- **`/app` library**: uploader, YouTube form, and in-flight upload tiles are removed from
  `library-experience.tsx`; gallery, playback, live analysis, and the (already admin-gated)
  export controls stay.
- **`/documents`**: the admin-only uploader and the `isAdmin` empty-state branch are removed;
  admins are pointed to the backoffice by copy, and the contextual reanalyse/delete controls
  on the viewer are untouched.

Isolation: each backoffice section is a self-contained component owning its hooks and API
client usage, so a future section (ingestion runs, users, flags) is an additive component, not
a rework. No new shared UI primitives are invented; styling follows the inline Tailwind
convention of the surrounding code.

### Approaches considered

- **Route section in the existing app (chosen)** — reuses session plumbing, nav, i18n, and the
  two-tier gate; smallest surface that satisfies "only possible from the backoffice".
- Separate admin app/subdomain — a second OIDC client, edge config, deploy target, and i18n
  stack for zero authorization gain (the same realm role would gate it). Rejected.
- Hide the uploader in place behind `isAdmin` (documents-style) — satisfies the letter but not
  the intent: no backoffice, admin ops stay scattered, and `/app` keeps mixed concerns.
  Rejected.

## Card breakdown

Cards 2 -> 3 -> 4 share the frontend shell/nav/experience files, so they are a stacked
dependency chain. Card 1 (Go) is file-disjoint and runs in parallel with card 2. Card 5
closes out.

1. **VER-205 Backend: admin-only video ingestion + video delete** — wrap
   uploads/youtube/confirm in `RequireAdmin`; add `DELETE /api/videos/{id}` (service + store +
   handler, documents pattern). Table tests: guest 403 / admin success for all four routes;
   delete service tests. (parallel-safe)
2. **VER-206 Backoffice foundation** — `/backoffice` route with server-side admin redirect,
   `BackofficeShell`, admin-only nav entry (`AppHeader` role prop), fr/en dictionary keys,
   empty sectioned scaffold. Tests: page redirect matrix, shell, nav gating. (parallel-safe)
3. **VER-207 Video ingestion moves to the backoffice** — videos section (uploader, YouTube
   form, upload tiles, management list with delete); strip ingestion UI from
   `library-experience.tsx`. Updates both surfaces' tests. (depends on VER-205, VER-206)
4. **VER-208 Document ingestion joins the backoffice** — documents section (PDF uploader,
   upload tiles); `/documents` becomes consumption-only. Tests updated. (depends on VER-207,
   shared files)
5. **VER-209 Docs + e2e close-out** — backoffice section in `docs/`,
   `docs/keycloak-prod-setup.md` note, README touch per the maintaining-documentation skill;
   full e2e sweep (below). (depends on VER-205, VER-208)

Sequencing note: after card 1 merges and until card 3 does, a guest briefly sees an uploader
that answers 403. Closing the backend gate first is the deliberate order — never ship a window
where the UI hides what the API still allows.

## Testing

- **Go**: table-driven, `go test -race ./...`. Handler auth-matrix tests (no token 401, guest
  403, admin 2xx) for the three gated mutations and the new delete; service-level delete tests
  over store + media fakes (object missing, store error, happy path).
- **Frontend**: Vitest, co-located. Page test mocks `next/headers` and asserts the non-admin
  redirect; shell/nav tests assert the backoffice entry renders for admin only; experience
  tests reuse the `stubBackend` helper for upload/import/delete flows; `/app` and `/documents`
  tests assert the ingestion UI is gone.
- **E2E (card 5, compose-based)**: as `admin` — backoffice visible, video upload, YouTube
  import, delete, PDF upload all succeed from `/backoffice`; as `guest` — no nav entry,
  `/backoffice` redirects to `/app`, direct `POST /api/videos/uploads` answers 403, gallery
  playback and live analysis still work; unauthenticated — `/backoffice` bounces to `/login`.
- Lint/format clean per the workspace standards (gofmt/gofumpt, go vet, golangci-lint,
  ESLint).

## Risks / notes

- Removing upload from `/app` changes the first-run experience for non-admin users: an empty
  gallery has no call to action. The seeded sample video keeps the gallery non-empty in dev;
  prod content is operator-curated by design — this epic is the mechanism.
- `AppHeader` gains a `role` prop touched by three shells; card 2 owns that change so cards 3
  and 4 rebase onto it rather than colliding.
- The unverified client-side role decode (`token.ts`) stays reveal-only; the server-side
  redirect in the backoffice page is UX, not security — every mutation is enforced by
  `RequireAdmin` on the backend.
