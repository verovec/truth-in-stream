# Backoffice (admin ingestion area)

Content ingestion is an operator task, so it is gathered in one admin-only area at `/backoffice`.
Consuming the content - watching a video with live fact-checking, reading an analysed document -
stays open to every authenticated user. Contextual admin actions that act on the thing in front of
you (re-run a document's analysis, delete a document, export a video's transcript) stay where they
apply, on the viewer, not in the backoffice.

The backoffice is a route section of the existing app, not a separate application or subdomain. It
reuses the app's session plumbing, navigation, i18n, and the two-tier admin gate; there is no second
OIDC client and no new Keycloak role.

## What lives in the backoffice

| Section | Actions |
|---------|---------|
| Videos | Upload a video file, import a YouTube link, watch ingest progress, and delete a video from the library. |
| Documents | Upload a PDF for analysis (text is extracted in the browser, unchanged - see [PDF fact-check](pdf-fact-check.md)). |

`/app` (the video library) and `/documents` are consumption-only: they list and play or read, with
no ingestion controls for anyone. A signed-in `admin` reaches the backoffice from a navigation entry
that only admins see.

## Access model

Two independent gates, both reused from the existing pattern rather than invented here:

- **Backend (authoritative).** Every ingestion mutation carries `middleware.RequireAdmin`: the video
  upload, confirm, YouTube-import, and delete routes, and the document upload and extraction routes.
  A caller whose verified Keycloak token does not carry the `admin` realm role gets `403`, whatever
  the UI shows. This is the real enforcement.
- **Frontend (reveal-only).** The `/backoffice` page resolves the session server-side and redirects
  a non-admin to `/app`, so a non-admin never receives the backoffice tree, and the nav entry renders
  only for admins. The route is private (absent from the proxy's public paths), so a cookie-less
  visitor is bounced to `/login`. This is UX: a forged cookie can reveal chrome but never unlock a
  mutation, because the backend rejects it independently.

## Granting admin access

The `admin` realm role is the single authorization switch; the backoffice ships no new role and no
Keycloak automation.

- **Local dev.** The seeded realm (`stack/keycloak/realm.json`) already ships an `admin` / `test1234`
  user carrying the `admin` role, plus a `guest` / `guest` user that exercises the deny path. Sign in
  as `admin` to see and reach `/backoffice`; sign in as `guest` and the entry is hidden and
  `/backoffice` redirects to `/app`. No realm change is needed.
- **Production.** Operators are created and role-assigned by hand in the Keycloak admin console.
  Assigning the `admin` realm role grants the backoffice along with the other admin-gated surfaces.
  See [Keycloak production setup](keycloak-prod-setup.md#5-create-the-real-accounts-in-the-truth-in-stream-realm).
