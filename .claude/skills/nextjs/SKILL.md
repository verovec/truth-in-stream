---
name: nextjs
description: Use when working on the Next.js frontend in stack/frontend - App Router, React 19, Tailwind v4, and the Next 16 caching model
---

# Next.js frontend (stack/frontend)

Next.js 16.2.x (App Router) · React 19.2.x · TypeScript · Tailwind CSS v4 · ESLint 9 flat config · Vitest.

`stack/frontend/AGENTS.md` warns that this Next.js has breaking changes vs older training data. When unsure of an API, read `node_modules/next/dist/docs/` or fetch current docs via Context7 before writing code.

## Server vs Client Components
- Default is **Server Component**. Add `'use client'` only at the leaf that needs browser APIs, React state hooks, or event handlers.
- Never put `'use client'` on a layout or shared wrapper - it converts the whole subtree to client rendering.
- Client cannot import Server; pass Server Components as `children`/props instead.

## Data fetching (Next 16 caching model)
Next 16 removed implicit caching - every `fetch` is dynamic (`no-store`) by default. Opt in explicitly:
- `fetch(url, { cache: 'force-cache' })` - static/indefinite.
- `fetch(url, { next: { revalidate: 60 } })` - time-based (ISR).
- `'use cache'` directive on a Server Component / async fn - compiler derives the cache key. Cannot call `cookies()`, `headers()`, or read `params`/`searchParams` inside a `'use cache'` fn.
- Invalidate in Server Actions: `revalidateTag(tag, 'max')` (stale-while-revalidate), `updateTag(tag)` (read-your-writes), `revalidatePath(path)`.

## Route handlers & Server Actions
- `app/api/.../route.ts` exports named method fns (`GET`, `POST`). Default dynamic.
- Server Actions: `'use server'`, prefer a dedicated `actions.ts`. Call `revalidate*`/`updateTag` after mutations, then `redirect()`. Invoke via `<form action={fn}>` or React 19 `useActionState`.

## Tailwind v4
CSS-first: no `tailwind.config.js`. `@import "tailwindcss"` + `@theme { --color-... }` in the main CSS file; tokens auto-generate utilities.

## Testing
Vitest (not Jest - Jest can't handle async Server Components or ESM cleanly). Config in `vitest.config.ts` (`environment: 'happy-dom'`, `globals: true`). `npm test` runs `vitest run`. Use `@testing-library/react` + `@testing-library/jest-dom`. Async Server Components can't be unit-rendered by RTL - cover those with E2E (Playwright).

## Lint/format
ESLint 9 flat config (`eslint.config.mjs`). If adding Prettier, use `eslint-config-prettier` to disable overlapping rules; never `eslint-plugin-prettier`.

## Pitfalls
1. Assuming `fetch` is cached (Next 15 behavior) - it is dynamic by default in 16.
2. `'use client'` too high in the tree.
3. Dynamic APIs (`cookies`/`headers`/`params`) inside a `'use cache'` fn - throws.
4. `revalidateTag` without the profile arg in 16.1+ - pass `'max'`.
5. Unit-testing async Server Components with RTL - won't render.
