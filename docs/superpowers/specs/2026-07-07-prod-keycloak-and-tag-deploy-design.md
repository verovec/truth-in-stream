# Production Keycloak + tag-triggered deploy (design)

Date: 2026-07-07
Status: Approved (design)

## Problem

The application is already Keycloak-native. VER-147 cut `/api/*` to a verified Keycloak identity
gate (`RequireIdentity`) and retired the legacy password login by default; the frontend runs the
full OIDC authorization-code + refresh flow against a Keycloak realm. Local dev provisions Keycloak
26.6.3 in docker-compose with a split-horizon networking contract (browser face `localhost:8081`,
back-channel face `keycloak:8081`) and imports `stack/keycloak/realm.json`.

**Production provisions no Keycloak at all.** `stack/terraform/prod` stands up the full edge (ACM +
CloudFront + WAF + internal ALB), RDS, RabbitMQ, Valkey, S3, ECS, IAM, and observability, but:

- There is no Keycloak module, service, database, DNS, or certificate coverage. `docs/infrastructure.md`
  documents prod Keycloak as "operator-managed out of band at `login.jeminforme.fr`, not provisioned
  by this terraform." That deferral blocks a self-contained deploy.
- The **frontend** prod service sets only `PORT` and `NEXT_TELEMETRY_DISABLED`; it has no
  `KEYCLOAK_*` / `NEXT_PUBLIC_APP_URL`, so the OIDC routes fall back to the `localhost:8081` dev
  defaults in `src/lib/auth/config.ts` and login is non-functional in prod.
- The **backend** prod service still injects the dead legacy `AUTH_EMAIL` / `AUTH_PASSWORD_HASH` /
  `SESSION_SECRET` secrets (read only when `LEGACY_PASSWORD_LOGIN=true`) and sets no `KEYCLOAK_*`, so
  `RequireIdentity` would validate tokens against the `localhost:8081` default issuer and every
  `/api/*` call 401s.

Separately, every deploy path is manual: the four `deploy-*.yml` workflows are `workflow_dispatch:`
only and `terraform.yml` auto-applies **dev** only (prod is "promote manually"). The goal is that
**pushing a version tag to `main` deploys the whole stack to AWS.**

## Goals

- Production Keycloak is provisioned entirely by Terraform: a self-hosted Keycloak on ECS Fargate,
  backed by a durable Postgres database, reachable at `https://login.jeminforme.fr`, with the realm
  imported at boot. A clean apply + tag brings it up with no out-of-band step beyond pushing secret
  values.
- The frontend and backend prod services are wired to that Keycloak so the OIDC login chain works
  end to end in prod, mirroring the dev split-horizon contract (public issuer, internal back-channel).
- Pushing a tag matching `v*` to `main` runs the full deploy in order: `terraform apply` prod ->
  build/scan/push images -> DB migrations + Keycloak DB bootstrap -> roll all services. The git tag
  is the deliberate human action; the prod `terraform apply` runs through a GitHub `production`
  Environment so an optional required-reviewer turns it into a one-click gate with no code change.
- S3 and RDS are reviewed and confirmed production-ready (no blockers found; minor hardening noted).

## Non-goals

- Enabling the production ingestion fleet (embed/crawl worker services + the worker-lifecycle
  lambda). It stays foundation-only / gated off; a separate follow-up owns it. The ingestor *code*
  review is owned by a separate agent.
- Migrating identity to a managed IdP (Cognito). The app stays Keycloak-coded.
- Declarative realm management via the Terraform Keycloak provider. The realm is imported at boot
  (idempotent) and users are managed in the admin console, matching dev. Revisit if realm drift
  becomes a burden.
- Multi-AZ / HA for Keycloak. A single Fargate task is the cost baseline; `desired_count` is a
  variable so redundancy is a one-line change once auth carries real traffic.

## Design

The work is one epic of six cards. Cards 1-3 and 5 touch `stack/terraform/prod/main.tf` and are a
**serial stacked chain** (delivered in order, each rebased on the previous), matching how the
VER-125/126 terraform cards ran. Card 4 (app wiring) and card 6 (e2e/docs) bracket them.

### Card 1 - Keycloak Terraform module + optimized image + prod realm

A new `stack/terraform/modules/keycloak` module: a Fargate service in the prod VPC private subnets,
behind the internal ALB, that runs Keycloak in production mode.

**Image.** A new `stack/keycloak/Dockerfile` `FROM quay.io/keycloak/keycloak:<pinned>` that runs
`kc.sh build --db=postgres --features=<...>` (bakes the Postgres JDBC + enabled features into an
optimized image) and copies the prod realm import file into `/opt/keycloak/data/import/`. The
container runs `kc.sh start --optimized --import-realm`. Pushed to a **new ECR repo `keycloak`**
(add `"keycloak"` to `prod/main.tf` `module.ecr.repositories`), built/scanned/pushed by the same
`_deploy.yml` engine as the other services.

**Runtime config** (production mode behind CloudFront -> internal ALB, plain HTTP inside the VPC):

- `KC_DB=postgres`, `KC_DB_URL_HOST` / `KC_DB_URL_DATABASE=keycloak` / `KC_DB_USERNAME` /
  `KC_DB_PASSWORD` (username/password from Secrets Manager; see Card 2).
- Hostname v2: `KC_HOSTNAME=https://login.jeminforme.fr` (full public URL), `KC_HOSTNAME_STRICT=true`.
- `KC_PROXY_HEADERS=xforwarded` so Keycloak trusts the `X-Forwarded-*` set by CloudFront/ALB (the
  distribution forwards them via the AllViewer origin-request policy). `KC_HTTP_ENABLED=true` for
  the plain-HTTP hop from the ALB.
- `KC_HEALTH_ENABLED=true`, `KC_METRICS_ENABLED=true`; health endpoints on the management port 9000
  (`/health/ready`, `/health/live`). Bootstrap admin (`KC_BOOTSTRAP_ADMIN_USERNAME` /
  `KC_BOOTSTRAP_ADMIN_PASSWORD`) from Secrets Manager for first-boot master-realm admin.

(The exact hostname/proxy/optimized-image/import flags are pinned to the current Keycloak 26 docs;
the research note for this epic records the verified values and the pinned patch version.)

Two topology-driven realm/runtime details: the prod realm sets `sslRequired: "none"` (not `external`)
because TLS terminates at CloudFront and the internal ALB forwards plain HTTP, so the ALB rewrites
`X-Forwarded-Proto` to `http`; with `external` and a public client IP Keycloak would reject every
login as "HTTPS required." HTTPS is still enforced at the edge (CloudFront `redirect-to-https`) and
the internal ALB is unreachable except through CloudFront, so this is safe. Build-time options
(`KC_DB`, health, metrics) live only in the Dockerfile (baked by `kc.sh build`); the task definition
carries only runtime options, so an `--optimized` image never sees a build/runtime mismatch. Keycloak
serves traffic on port 8081 (a distinct port on the shared ECS-tasks security group, since the backend
already owns the 8080-from-ALB ingress rule) with health on the management port 9000.

**ALB wiring.** The module owns its own target group + a **host-header** listener rule
(`host_header = ["login.jeminforme.fr"]`) at a priority below the backend's (< 10) so it wins over
the frontend `/*` catch-all, forwarding to the Keycloak target group. Health check hits port 9000
`/health/ready`. Its task SG takes ingress from the ALB SG on the container port, same least-privilege
pattern as the `service` module. (A dedicated module rather than reusing `service`, which is
path-pattern-only and single-port.)

**Realm file.** A prod realm JSON (see Card 4 for the redirect-URI/origin shape) imported at boot.
Import is idempotent (an existing realm is skipped on later boots), so first deploy seeds it and
subsequent deploys do not clobber admin-console changes; a realm change is re-applied by an explicit
re-import, documented as a known limitation.

The module is added to `prod/main.tf` gated by `enable_keycloak` (default `true` in prod) and wired
into `module.iam` (task exec role reads the new secrets) and `module.apply_permissions`
(`include_keycloak`). `enable_keycloak = false` keeps a clean plan for anyone who runs Keycloak
elsewhere.

### Card 2 - Keycloak database (dedicated DB on the existing RDS) + bootstrap task

Keycloak needs a durable Postgres database that **pre-exists** (Keycloak creates its tables but not
the database). The CI Terraform runner cannot reach the private RDS, so a one-shot in-VPC Fargate
task creates it, mirroring the `migration` module pattern.

- A `random_password` for a scoped `keycloak` role + a new Secrets Manager secret holding
  username/password (never in Terraform state values beyond the generated password, consistent with
  the RDS module's own generated master password).
- A `keycloak-db-bootstrap` one-shot task (new small module or an extension of `migration`) that
  connects with the RDS **master** DSN and runs idempotent SQL: create the `keycloak` role if
  absent (password from the keycloak secret), `CREATE DATABASE keycloak OWNER keycloak` if absent.
  Runs in the deploy pipeline before the Keycloak service rolls (Card 5).
- The Keycloak service (Card 1) consumes the scoped keycloak credentials, not the master DSN.

Least-privilege: the app and Keycloak use separate roles on the same instance; a compromised app DB
role cannot read the auth database. Cost baseline: one RDS instance. Isolation upgrade path (a
dedicated RDS instance) is a future variable flip, not a rewrite.

### Card 3 - Edge for `login.jeminforme.fr` (cert SAN + CloudFront alias + DNS)

Reuse the existing single distribution + internal ALB; no second distribution needed because the
distribution already forwards `Host` and the ALB host-routes.

- **ACM** (`prod/main.tf` `module.acm`): add `login.${var.domain_name}` to
  `subject_alternative_names` (cert now covers apex + www + login). The main-account root already
  `for_each`es over the cert's `domain_validation_options`, so the new validation CNAME is created
  automatically with no further main-account cert change.
- **CloudFront** (`module.cloudfront` `aliases`): add `login.${var.domain_name}`. The cert covers
  it; the AllViewer origin-request policy already forwards `Host`, so `login.` requests reach the
  ALB with their original Host and hit the Keycloak host-header rule from Card 1.
- **main-account DNS**: add `login.${var.domain_name}` to `local.alias_fqdns` so the A/AAAA aliases
  to the CloudFront distribution are created alongside apex/www.

Admin-console exposure note: the login distribution shares the app's WAF (managed rules + per-IP
rate throttle), which also protects the Keycloak login and admin endpoints. Scoping a stricter WAF
to `login.` (or splitting to a dedicated distribution) is a documented later option, not needed for
first prod.

### Card 4 - App prod wiring + prod realm shape

- **Backend service** (`prod/main.tf` `module.backend`): add env `KEYCLOAK_ISSUER =
  "https://login.jeminforme.fr/realms/truth-in-stream"` and `KEYCLOAK_JWKS_URL` pointing at the
  internal Keycloak (via the internal ALB host or the Keycloak service), so tokens validate against
  the public issuer while JWKS is fetched inside the VPC - exactly the dev `KEYCLOAK_JWKS_URL`
  override seam. Remove the dead legacy `AUTH_EMAIL` / `AUTH_PASSWORD_HASH` / `SESSION_SECRET` from
  the serving task's `secrets`; keep the secret containers behind a new `enable_legacy_password_login`
  (default `false`) so re-enabling the legacy login is a variable flip, not a code change.
- **Frontend service** (`module.frontend`): add env `KEYCLOAK_ISSUER`, `KEYCLOAK_CLIENT_ID =
  "truth-in-stream-web"`, and `KEYCLOAK_INTERNAL_URL` (internal Keycloak base) for the server-side
  discovery + back-channel token/refresh calls, mirroring the dev split-horizon.
- **`NEXT_PUBLIC_APP_URL`**: the frontend Dockerfile bakes no build-arg for it, so the OIDC redirect
  URIs default to `localhost:3000`. Add `ARG NEXT_PUBLIC_APP_URL` + `ENV NEXT_PUBLIC_APP_URL` in the
  build stage and pass `--build-arg NEXT_PUBLIC_APP_URL=https://jeminforme.fr` from the frontend
  deploy workflow, and also set it as a serving-task env var. The e2e check (Card 6) confirms the
  emitted `redirect_uri` is `https://jeminforme.fr/auth/callback`, not localhost.
- **Prod realm shape**: `redirectUris = ["https://jeminforme.fr/*"]` and web origins
  `["https://jeminforme.fr"]` on the `truth-in-stream-web` public PKCE client (no client secret -
  matches the app). Keep the `admin`/`guest` roles and the `guest` default role; drop the dev users
  (real operators are created in the admin console). The dev `realm.json` (localhost) is unchanged;
  the prod realm file is separate or the localhost values are parameterized - decided in
  implementation, preferring a distinct prod file for clarity.

### Card 5 - Tag-triggered deploy pipeline + policy/docs update

A new `.github/workflows/deploy.yml` on `push: tags: ['v*']` that orchestrates the whole deploy,
reusing the existing reusable workflows:

1. **`terraform apply` prod** via `_terraform.yml` (or a prod variant) with `apply: true`, guarded by
   the existing `terraform plan` + `scripts/iam-apply-guard.sh`. Runs under a GitHub `production`
   Environment: automatic by default (matches "deploy directly"), one-click if a required reviewer is
   added in repo settings - no code change to switch. Prod RDS/ALB already carry `deletion_protection`
   and the RDS a final snapshot, bounding a bad apply's blast radius.
2. **Build/scan/push images** (backend, frontend, keycloak, migrate) via `_deploy.yml` build steps
   (Trivy HIGH/CRITICAL gate stays).
3. **DB migrations + Keycloak DB bootstrap** (the `migrate` task and the Card 2 bootstrap task).
4. **Roll services** (backend, frontend, keycloak) via the existing rolling deploy step.

The four existing `deploy-*.yml` stay `workflow_dispatch` for targeted single-service rolls; the new
tag pipeline is the "deploy everything" path.

**Policy reconciliation.** This changes the standing rule. Update CLAUDE.md's "Deploys stay
human-gated" / "workflow_dispatch-only" always-on rule to: a version tag on `main` is the deliberate
human action that triggers a full deploy; the prod `terraform apply` runs through the `production`
Environment. Update `docs/infrastructure.md` (remove the "operator-managed out of band" Keycloak
paragraph; document the self-hosted Keycloak + the tag-deploy flow) and `docs/configuration.md` (prod
`KEYCLOAK_*`, the new Keycloak/DB secrets, the deploy tag convention).

### Card 6 - E2E, smoke, and documentation close-out

- Extend the CI `keycloak_smoke` integration guard where it makes sense to also assert the prod
  config seam (issuer/JWKS split resolves), keeping the existing compose-based login-chain test.
- A `terraform plan` of `stack/terraform/prod` is green on CI (the `terraform.yml` PR job already
  plans dev; add/confirm prod plan coverage for the new modules) with `iam-apply-guard` satisfied by
  the extended `apply_permissions` manifest.
- An end-to-end check of the deploy artifacts (image builds succeed, frontend build bakes the prod
  app URL, terraform plan clean) stands in for a live apply, which stays human/tag-gated.
- Documentation updated per Card 5; the maintaining-documentation skill governs the README/docs pass
  once the epic's final card merges.

## S3 / RDS review (confirmation)

No blockers. RDS: single-AZ cost baseline with `deletion_protection`, 21-day backups, final
snapshot, encryption at rest, private-only, generated master password in Secrets Manager - all
production-appropriate; Multi-AZ is a documented variable flip. S3: media + backup buckets are
provisioned; media CORS defaults to `*` (tighten `media_cors_allowed_origins` to the app origin now
that a fixed domain exists - a minor hardening folded into Card 4). Keycloak adds a scoped DB role on
the existing instance (Card 2), no RDS topology change.

## Testing

- **Terraform**: `terraform fmt -check`, `validate`, and a green `plan` on the prod root through CI;
  `iam-apply-guard` passes against the extended `apply_permissions` (with `include_keycloak`).
- **Go/Frontend**: the `keycloak_smoke` guard stays green; backend `go test -race ./...` and frontend
  Vitest cover any config changes to the identity seam.
- **Image builds**: backend, frontend (with the prod `NEXT_PUBLIC_APP_URL` build-arg), keycloak, and
  migrate images build and pass the Trivy HIGH/CRITICAL scan.
- **E2E**: for each terraform card, a clean `plan` with the new resources; for card 4, the emitted
  OIDC `redirect_uri`/issuer resolve to the prod domain, verified without a live apply.

## Rollout / ordering

Deliver serially: Card 1 -> 2 -> 3 (stacked terraform), then Card 4 (app wiring), then Card 5 (tag
pipeline), then Card 6 (e2e/docs). Each card is code-reviewed and merged to `main` before the next
rebases on it. The first real production apply + tag is a human/operator action outside this epic;
these cards make it a single `terraform apply` (or tag) away.
