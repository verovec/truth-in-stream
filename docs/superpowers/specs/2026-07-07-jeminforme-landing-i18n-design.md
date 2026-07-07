# jeminforme.fr - landing rework + FR/EN i18n foundation

Date: 2026-07-07
Status: Approved (brainstorm), ready for implementation

## Purpose

Rebrand the public landing page from "Truth in Stream" to **jeminforme.fr** and make it
French-first, with a French/English switch. The page must communicate the product honestly:
real-time, source-backed fact-checking of French and EU political debate. Visual direction
follows the clarity and structure of synquery.ai (whitespace, value-prop hero, checkmark
pillars, numbered `01/02/03` process, a workflow/data-callout visual, closing CTA) while
staying true to the civic, non-partisan purpose of the project.

## Scope

In scope:
- The public landing page and its shared marketing chrome (header + footer).
- A reusable i18n foundation (`fr` default, `en` secondary) the rest of the app can adopt later.
- A new inline-SVG logo (tricolore verification mark) and the `jeminforme.fr` wordmark.
- `/fr` and `/en` URLs; `/` redirects to the French default.

Out of scope (explicitly, to protect sensitive wiring - later cards):
- The authenticated app (`/app`), the login/auth screens (`/login`, `/auth/*`), the `@auth`
  parallel-route slot, the Keycloak/OIDC flow, and the Next.js proxy rewrites. None are moved,
  renamed, or restructured. Their copy stays as-is for now.
- Translating logged-in application UI.

## Constraints discovered in the codebase

- The `@auth` parallel slot and the `/login` modal interception (`app/@auth/(.)login`) are
  anchored at the **root layout**. The landing's "Open the app" link depends on this
  interception. Therefore the root layout and the app/auth routes must not be restructured.
- Backend access is via `next.config.ts` rewrites (`/api/*`, `/demo/*`); the standalone build
  freezes them at build time. i18n must not interfere with these paths.
- Next.js here is 16.2.x (App Router, React 19, Tailwind v4). Per `stack/frontend/AGENTS.md`,
  verify i18n specifics against `node_modules/next/dist/docs` before coding.

## Architecture

### Routing / i18n (no middleware)

A `[locale]` segment scoped to the marketing surface only, coexisting with the untouched app:

```
app/
  layout.tsx                 # root layout - unchanged except <html lang> default -> "fr"
                             #   and site-wide default metadata -> jeminforme.fr
  page.tsx                   # server component: negotiate locale (Accept-Language, fr wins)
                             #   and redirect() to /fr or /en
  [locale]/
    layout.tsx               # validate locale (notFound() if unknown); load dictionary;
                             #   render marketing chrome (Header + Footer); set client <html lang>
    page.tsx                 # the reworked landing, strings from the dictionary
  login/  app/  auth/  @auth/   # UNCHANGED
```

- `generateStaticParams` returns `[{locale:'fr'},{locale:'en'}]` so both are statically
  generated. Any other value -> `notFound()` (guarded in the `[locale]` layout).
- No `middleware.ts`. `/` -> locale via a server `redirect()` in `app/page.tsx`. This is lower
  risk than middleware (which would have to be carefully scoped away from `/api`, `/auth`, etc.).
- Static routes (`/login`, `/app`, `/auth/*`) win over the dynamic `[locale]` segment in Next's
  route resolution, so they are never captured as a "locale".
- The auth proxy (`src/proxy.ts`) redirects every non-public path to `/login`. Its public
  allowlist held only `/`, so `/fr` and `/en` must be added, derived from the `locales` config.
  This is the auth gate, distinct from the untouched `next.config.ts` `/api` and `/demo`
  rewrites. (Discovered during the e2e check: without it, `/fr` 307s to `/login`.)
- `<html lang>`: the shared root layout renders `lang="fr"` (site default). The `/en` marketing
  layout corrects `document.documentElement.lang = 'en'` on the client. This keeps the shared
  root layout static and untouched by per-request locale, at the cost of a one-tick client
  correction for the English page - an accepted trade-off to avoid disturbing `@auth`/root.

### i18n foundation

```
lib/i18n/
  config.ts          # locales = ['fr','en'] as const; defaultLocale = 'fr'; type Locale;
                     #   isLocale(x) guard; negotiate(acceptLanguage) -> Locale (fr wins)
  dictionaries.ts    # 'server-only'; getDictionary(locale): Promise<Dictionary> via dynamic import
  dictionaries/
    fr.ts            # source of truth for the Dictionary shape (typed)
    en.ts            # satisfies Dictionary - compile-time parity with fr
```

- The dictionary is a nested object grouped by surface: `nav`, `hero`, `pillars`, `how`,
  `mission`, `cta`, `footer`, `meta` (title/description for `<Metadata>`).
- `en.ts` is typed `satisfies Dictionary` so a missing/renamed key fails the build - key parity
  is enforced by the type system, and also asserted by a unit test.
- All French copy uses correct diacritics (e-acute, e-grave, a-grave, c-cedilla, oe) and
  typographic apostrophes where natural in prose; ASCII apostrophes are acceptable in code/keys.

### Logo

`components/marketing/logo.tsx` - a presentational (non-client) component:
- Inline SVG: a checkmark whose two strokes carry the tricolore (bleu `#0055A4`, rouge
  `#EF4135`) separated by a white notch, on a subtle rounded backdrop that adapts to
  light/dark. Accessible `<title>` ("jeminforme.fr"). `size` prop; crisp from ~20px.
- Rendered beside the `jeminforme.fr` wordmark in the header; France colors are used as accents
  only, elsewhere the palette stays neutral zinc so the page reads credible and non-partisan.

### Components

```
app/[locale]/_components/
  header.tsx             # logo, anchor nav, language toggle, "Ouvrir l'application" -> /login
  footer.tsx             # brand, tagline, minimal links, language toggle, (c) jeminforme.fr
  language-toggle.tsx    # 'use client'; swaps between /fr and /en for the current path
  hero.tsx               # eyebrow pill, headline, subheadline, dual CTA, verdict-card visual
  verdict-card.tsx       # the synquery-style claim -> verdict + sources visual (tricolore chips)
  pillars.tsx            # 3 checkmark trust pillars
  how-it-works.tsx       # numbered 01/02/03 process
  mission.tsx            # civic "responsabilite" band
  closing-cta.tsx        # final call to action
components/marketing/
  logo.tsx
```

Server Components by default; only `language-toggle.tsx` is `'use client'` (needs `usePathname`
/ router to swap the locale prefix). Each section takes its already-resolved strings as props;
none fetch or import the dictionary directly, so they are trivially unit-testable.

### Landing content (FR default; EN mirrors)

1. Header - logo, nav (anchors), FR/EN toggle, "Ouvrir l'application".
2. Hero - pill "Verification des faits en temps reel"; headline on putting facts at the centre
   of political debate; subheadline; CTA "Ouvrir l'application" + "Voir comment ca marche";
   verdict-card visual.
3. Piliers de confiance - Source, Temps reel, Sur les faits (each with a checkmark).
4. Comment ca marche - `01` Ecoute, `02` Recherche, `03` Verdict.
5. Mission / responsabilite - short civic band on informing democratic debate.
6. CTA de cloture.
7. Footer.

(French copy in code carries full diacritics; ASCII used here only to satisfy the repo's
non-ASCII guard in this doc.)

## Testing (Vitest, TDD)

- `config.test.ts`: `isLocale`, `negotiate` (fr default, en when preferred, fr on unknown).
- `dictionaries.test.ts`: `getDictionary('fr'|'en')` returns the surface; fr/en key sets match.
- `logo.test.tsx`: renders an accessible SVG (has a title / role img name).
- `language-toggle.test.tsx`: given a pathname, links to the other locale's equivalent path.
- Landing/section tests: render with the `fr` dictionary -> French headings; with `en` -> English.
- `app/page` redirect test: `/` triggers a redirect to `/fr` (default).
- Update the existing `app/page.test.tsx` (currently asserts the old English landing) to cover
  the new redirect behaviour instead.
- `generateStaticParams` returns fr + en.
- Metadata is localized (title/description come from the dictionary `meta`).

Green bar required: `vitest run`, `eslint`, `tsc`. `next build` runs on CI (Turbopack rejects the
symlinked worktree `node_modules`, a known local limitation).

## End-to-end check

Run the frontend dev server in the worktree and confirm:
- `/` redirects to `/fr`; `/fr` shows the French landing; `/en` shows the English landing.
- The FR/EN toggle switches languages and the URL; `<html lang>` reflects the locale.
- "Ouvrir l'application" navigates to `/login` (interception/login still works).
- Light and dark render cleanly; no horizontal scroll on mobile widths.

## Risks and mitigations

- Dynamic `[locale]` capturing unintended paths -> static app routes take priority; unknown
  locales `notFound()`; no middleware. Verified by routing tests + e2e.
- `<html lang>` for `/en` corrected client-side -> accepted, documented above; alternative
  (per-request header read in root layout) rejected because it forces the whole app dynamic.
- Interception regression -> e2e explicitly checks the `/login` modal from `/fr`.
