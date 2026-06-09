---
name: nextjs
description: Use when writing, reviewing, or refactoring any frontend code in stack/frontend - components, routes, Server Actions, caching, Tailwind, fonts/images, tsconfig, or Vitest/Playwright tests
---

# Next.js frontend (stack/frontend)

Next.js 16.2.x (App Router, Turbopack default) · React 19.2.x · TypeScript strict · Tailwind v4 · ESLint 9 flat · Vitest.

These rules are MUST/NEVER. The bar is the smallest client bundle, zero waterfalls, and a component tree a stranger can navigate - not "it renders". This Next.js breaks older training data: when unsure of an API, fetch current docs via Context7 before writing code, never from memory.

## Component model (server-first, strictly)

- Everything is a Server Component until proven otherwise. `'use client'` goes at the **leaf** that needs state, effects, event handlers, or browser APIs - never on a layout, page, or shared wrapper. Hoisting it is a review finding.
- A Client Component never imports a Server Component - pass Server Components down as `children`/props through the client boundary.
- Module poisoning guards are mandatory: any module touching secrets, the backend URL with credentials, or DB access imports `server-only`; browser-only modules import `client-only`. A missing guard is a latent leak.
- One component per file, named after the file. Shared UI lives in `components/`, route-private pieces colocated with the route in `_components/`. No barrel files (`index.ts` re-export hubs) - they defeat tree shaking and Turbopack chunking.
- `error.tsx` is necessarily a Client Component; every route segment that fetches gets `loading.tsx` or an explicit `<Suspense>` - slow data must never block the shell.

## Component construction (how every component is shaped)

- **House pattern: server container + client leaf.** The Server Component fetches and shapes data; the Client Component receives plain serializable props and owns only interaction. Logic never lives in JSX - a component is markup over already-computed values.
- **Composition over configuration.** Extend components by accepting `children` and named slots (`header`, `actions`), not by adding boolean props. Three `isX` props on one component (`isCompact`, `isInline`, ...) is a redesign signal - that is what `cva` variants or compound components are for.
- **Compound components** for families that share state (`<ClaimCard>` + `<ClaimCard.Verdict>` + `<ClaimCard.Sources>`): the consumer composes, the family coordinates internally.
- **Make invalid states unrepresentable.** Props and state are discriminated unions (`{ status: 'verified', verdict: Verdict } | { status: 'pending' }`), never sibling booleans (`isLoading` + `isError` + `data?`) that allow contradictions. Exhaustive `switch` on the discriminant; the compiler catches the missing case.
- **Custom hooks** (`useX` in `hooks/`) extract any reusable client logic; a component with more than trivial imperative code inside it is a hook waiting to be extracted.

### Dynamic by data, not by duplication

Adding an item must mean adding data, not copying JSX:

- Navs, tables, dashboards, and option lists render from typed config arrays (`const NAV: NavItem[]`); the component maps, the data varies.
- Forms are driven by their Zod schema: the schema is always the validator (Server Action) and the type source (`z.infer`) - no exceptions. Deriving the field-rendering config from it too is optional, but a hand-maintained second list of the form's fields is banned: the form's shape exists in exactly one place.
- Variants are `cva` definitions, not parallel component copies. A second `ButtonSecondary.tsx` is a finding.
- **Prop drilling through more than one intermediate component** (a layer that forwards a prop without using it) means the composition is wrong - restructure so the data-owning Server Component renders the consumer directly (pass components, not props, through layers). React Context only for genuine cross-cutting *client* state (theme, session presence), created at the lowest scope that needs it - never as a data-fetching channel.
- TypeScript generics on components/hooks only when they delete real duplication (`<DataTable<Claim>>`); generic-for-the-sake-of-it is noise the next reader pays for.

## Data fetching (zero-waterfall rule)

- Independent requests start together: `Promise.all` or start-the-promise-early-await-late. A sequential `await` chain of independent fetches is a bug, not a style choice.
- Deduplicate per-request work with `React.cache()` around data-access functions; use the preload pattern (`export const preload = (id: string) => { void getItem(id) }`) when a parent knows what a child will need.
- Stream anything slow behind `<Suspense fallback={...}>` - page-level via `loading.tsx`, component-level inline.
- All data access goes through one data-access layer (`lib/data/`), not inline `fetch` scattered through components. Authorization lives in the DAL, so every entry point - page, Server Action, AND `route.ts` handler - is covered by construction; an entry point that bypasses the DAL is the bug.

## Caching (Cache Components model - one mental model only)

Next 16 removed implicit caching: every `fetch` is dynamic by default. Standardize on `'use cache'` for anything cacheable; do not mix in legacy `fetch` cache options on new code.

- `'use cache'` on the function or component; lifetime with `cacheLife('hours' | 'days' | 'max' | custom-profile)`; key invalidation handles with `cacheTag(tag)`.
- Inside `'use cache'`: no `cookies()`, no `headers()`, no `params`/`searchParams` reads - it throws.
- Invalidation from Server Actions: `updateTag(tag)` only inside the action handling the current user's own mutation (read-your-writes); any change not initiated by the current user (background jobs, other users' verdicts, webhooks) uses `revalidateTag(tag, 'max')`; `revalidatePath` only when a tag does not exist yet.
- PPR comes via `cacheComponents: true` in `next.config.ts` - there is no `experimental.ppr` flag anymore.
- `middleware.ts` is deprecated since 16.1: the file is `proxy.ts` (Node runtime). Only redirects, rewrites, header mutation, and optimistic auth gating (session-cookie presence -> redirect, nothing more) belong there. Real authorization happens in the data-access layer; any DB or API call in `proxy.ts` is a finding.

## Server Actions

- Actions are *defined* only in `'use server'` files (`actions.ts` per feature), never inside a component file; importing one and passing it to `<form action={...}>` is the intended use.
- Every action, in order: auth check first, then Zod `safeParse` on the input, then the mutation, then `updateTag`/`revalidateTag`, then `redirect()`. No step is skippable; an action is a public HTTP endpoint regardless of where it is imported.
- Return typed state consumed by `useActionState` (imported from `react`, not `react-dom`); pending UI via `useFormStatus` or the action's pending flag. Optimistic updates via `useOptimistic` - never hand-rolled state mirrors.

## Performance (every line costs the user something)

- React Compiler is stable in 16: enable `reactCompiler: true` in `next.config.ts`. Manual `useMemo`/`useCallback` only with a React DevTools profiler trace showing the compiler missed, referenced in the PR - a comment asserting it is not evidence.
- `next/image` always, with explicit `width`/`height` or `fill` (CLS = 0 budget), and `priority` on the single LCP image.
- `next/font` for all fonts - no `<link>` to font CDNs.
- Heavy browser-only libs load via `dynamic(() => import(...), { ssr: false })`; check impact with the Turbopack bundle analyzer (16.1+).
- Budgets: INP < 200ms (thin handlers, `startTransition` for non-urgent updates), CLS < 0.1, LCP < 2.5s. New client-side dependencies default to rejected: the PR must state the gzipped KB cost and demonstrate (not assert) that no server-side, platform, or RSC alternative exists.
- `typedRoutes: true` in `next.config.ts` - invalid `href`s fail the build, not the user.
- `params`/`searchParams` are Promises in 16: `await` them in async components, `use()` in Client Components.

## Tailwind v4

- CSS-first: tokens in `@theme { --color-... }` in the entry CSS; `@theme` for values that should generate utilities, `:root` for plain variables that should not. No `tailwind.config.js`.
- Variants via `cva` with one shared `cn()` (clsx + tailwind-merge) exported from a single module - never string-concatenate class names.
- `@apply` is limited to base-layer resets. A component built from `@apply` blocks is a rewrite.

## TypeScript

`strict: true`, `moduleResolution: "bundler"` - never weaken the scaffolded tsconfig. No `any` (use `unknown` and narrow), no non-null `!` outside tests, no `as` casts where a type guard works. Zod schemas are the single source of truth for external data shapes - `z.infer` the types, never duplicate them by hand.

## Testing

- Vitest (`happy-dom`, `vitest run` via `npm test`): Server Actions and the data-access layer as plain async functions, Zod schemas, sync Server Components, and Client Components via `@testing-library/react`.
- Async Server Components cannot be rendered by RTL/Vitest: extract the data logic into a tested plain function and keep the component a thin wrapper; cover the rendered result with Playwright E2E.
- MSW v2 for network interception in component tests. New behaviour ships with its tests in the same diff - no exceptions.

## Red flags - stop and fix

- `'use client'` on a layout/page "to keep it simple" - move it to the leaf.
- `useEffect` + `fetch` for initial data - that is a Server Component's job.
- Sequential `await`s on independent data; a fetch outside the data-access layer.
- An action without auth-then-validate at the top; `revalidateTag` without its profile arg.
- A new `useMemo` with the compiler on and no DevTools evidence - and delete existing unjustified `useMemo`/`useCallback` in any component you touch; `console.log` left in; `npm i` of a client lib that duplicates a platform/RSC capability.
