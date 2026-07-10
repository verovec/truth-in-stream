# Production Keycloak setup runbook

One-time operator runbook for standing up the self-hosted production Keycloak after the first
production deploy. Everything here is a manual, human-gated action; the automated tag deploy provisions
the infrastructure but does **not** create the real operator accounts (the production realm ships
`users: []` by design). Run this once, after the first `v*` tag has deployed.

## Fixed facts

| Property | Value |
|----------|-------|
| App URL | `https://jeminforme.fr` |
| Keycloak URL | `https://login.jeminforme.fr` |
| Issuer | `https://login.jeminforme.fr/realms/truth-in-stream` |
| Admin console | `https://login.jeminforme.fr/admin` |
| Realm | `truth-in-stream` |
| AWS account | `<AWS_ACCOUNT_ID>` (the app account; real value lives in the gitignored `deploy/targets.json` / `*.tfvars` / operator env, never committed) |
| Region | `eu-west-3` |
| Deploy trigger | push a `v*` semver tag whose commit is on `main` (fires `release.yml`) |

Keycloak is gated on `enable_keycloak && enable_rds`: the identity provider stores its realm and users
in a dedicated `keycloak` database on the shared RDS instance, so RDS must be up first.

## Steps

### 1. Prerequisite: RDS is enabled

Confirm `enable_rds = true` and `enable_keycloak = true` in `stack/terraform/prod` (both default true in
prod). Keycloak's Fargate service, its scoped DB, and its secrets are created only when both are set;
with `enable_keycloak = false` none of the Keycloak resources are planned and Keycloak must be run out of
band.

### 2. Deploy

Push the app secrets, then tag a release and let `release.yml` do the rest:

```
git tag v1.4.0            # commit must be on main
git push origin v1.4.0
```

`release.yml` applies `stack/terraform/prod` (behind the `production` GitHub Environment approval), then
rolls the services in order: **backend** (with migrations) -> **Keycloak** (runs its idempotent DB-bootstrap
task, then rolls) -> **frontend**. Terraform generates the Keycloak bootstrap admin password and the scoped
`keycloak` DB role password and writes both to Secrets Manager; there is no out-of-band push for either.

### 3. Read the generated bootstrap password

Terraform stores the bootstrap admin password in Secrets Manager. Read it with:

```
aws secretsmanager get-secret-value \
  --secret-id truth-in-stream/prod/keycloak/bootstrap-admin-password \
  --region eu-west-3 \
  --query SecretString --output text
```

The value never leaves Secrets Manager / encrypted Terraform state; do not paste it into any committed
file or ticket.

### 4. First admin-console login

Open `https://login.jeminforme.fr/admin` and sign in as the bootstrap admin. The username is the
`keycloak_admin_username` Terraform variable (`KC_BOOTSTRAP_ADMIN_USERNAME`, default `admin`) and the
password is the value read in step 3. This bootstrap account lives in the **master** realm; it is a
temporary provisioning credential, not the app-login admin.

### 5. Create the real accounts in the `truth-in-stream` realm

Switch to the `truth-in-stream` realm (top-left realm selector), then:

1. **Users -> Add user**: create the real admin user (username, email, email verified on).
2. **Credentials -> Set password**: set a strong password and turn **Temporary off** so it is not forced
   to reset on first login.
3. **Role mapping -> Assign role**: assign the `admin` realm role. This is what grants the debug tools,
   the [backoffice](backoffice.md) ingestion area, and the `admin`-gated routes (the backend and
   frontend read `realm_access.roles`).
4. Confirm `guest` is the realm **default role** (Realm settings -> User registration -> Default roles);
   every new user carries `guest` automatically.
5. Create any other operator users the same way (assign `admin` only to those who need it; everyone else is
   a `guest`).

### 6. Security follow-up: retire the bootstrap admin

The bootstrap admin is a temporary master-realm provisioning account. Once a permanent admin exists in the
`truth-in-stream` realm and can manage the deployment, rotate or disable the bootstrap admin (master realm
-> Users -> the bootstrap user -> disable, or reset its credential) so the generated first-boot password is
no longer a standing credential.

### 7. Verify end to end

- Sign in at `https://jeminforme.fr` as the real admin user; you should land authenticated with the
  `admin` role.
- Confirm `/api/*` responds (the whole `/api` subtree is gated on the verified Keycloak identity) and that
  the admin-only debug tools and the [backoffice](backoffice.md) are visible to the `admin` user.
- Sign in as a `guest` user and confirm the debug tools are hidden, `/backoffice` redirects to `/app`, and
  `admin`-only routes return `403`.

## Realm re-import idempotency

The production realm (`stack/keycloak/realm-prod.json`, baked into the optimized Keycloak image) is
imported on **first boot only**: the realm, roles, default role, and the `truth-in-stream-web` client are
created once and are not re-applied on subsequent rolls, and prod seeds **no** users. Users you create in
the admin console therefore persist across deploys. A change to the realm shape (a new role, a client
setting) needs an explicit re-import (import strategy override) or a manual admin-console edit; simply
editing `realm-prod.json` and rolling does not re-apply it to an already-initialized realm.
