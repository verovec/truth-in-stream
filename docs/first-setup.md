# First setup

The ordered path from a clean AWS account to a production stack with data behind it.
Part 1 covers everything that must happen before and around the first deploy; part 2
covers getting fact-check data flowing (crawling and ingestion). Each step links to
the runbook that owns its detail — this page owns only the ordering.

Production stays human-gated by design: PRs only validate, a merge to `main`
auto-applies only the `dev` root, and the first applies, the DNS publication, every
production apply, and every deploy are deliberate human actions. Local development
needs none of this — see the [README quick start](../README.md#quick-start).

## Part 1 — before deploying to AWS

### 1. Local tooling and configuration

- Install Terraform (>= 1.11), AWS CLI v2 with the Session Manager plugin (used
  by the SSM tunnels), Docker Engine + Compose v2, GNU make, and `jq` (required
  by the ingestion tooling). `make doctor` verifies the Docker and make basics
  only — it does not check the rest.
- Configure the AWS SSO profile the terraform tooling uses, once per machine:
  `aws configure sso --profile truth-in-stream-dev` — see
  [AWS SSO profile](../stack/terraform/README.md#aws-sso-profile). The secret-push
  script defaults to its own profiles (`verovec-dev` / `verovec-prod`, overridable
  via `SECRETS_DEV_PROFILE` / `SECRETS_PROD_PROFILE`); the database scripts default
  to the same `verovec-*` names and are overridden via `AWS_PROFILE`.
- Copy `.env.example` to `.env` and fill the real API keys — at minimum
  `EMBEDDING_API_KEY` (Voyage) and `TRANSCRIPTION_API_KEY` (AssemblyAI). The file is
  gitignored and later feeds `make push-secrets`. Clear the demo
  `AUTH_PASSWORD_HASH` the example ships: `push-secrets` pushes every non-empty
  allowlisted key, and with legacy password login off (the default in both
  environments) a non-empty value creates an unmanaged secret holding that
  committed hash. Key inventory:
  [Configuration](configuration.md#environment-variables).
- Copy `deploy/targets.example.json` to `deploy/targets.json` and fill the real
  account ids. The file is gitignored; the account guard
  (`scripts/aws-target-guard.sh`) refuses every ingestion run until the placeholder
  ids are replaced. The database scripts do not read it — they target whatever
  account the active profile resolves to, so check the profile before tunnelling.

### 2. Bootstrap the Terraform state backend (once)

```sh
./scripts/bootstrap-tfstate.sh
```

Creates the `truth-in-stream-tfstate` bucket with versioning (required for native
S3 state locking), default encryption, and a full public-access block. Idempotent.
Detail: [Remote state](../stack/terraform/README.md#remote-state).

### 3. First apply of `dev` (human, elevated credentials)

```sh
cd stack/terraform/dev && terraform init && terraform apply
```

- The first-ever apply of a fresh account cannot run from CI: it creates the IAM
  the CI roles need, so a human runs it with elevated credentials. Every later
  apply that introduces permissions the CI apply role lacks is the same kind of
  deliberate elevated run — CI's pre-apply guard names the missing actions.
- The `dev` root owns the account-global GitHub OIDC provider; `prod` only
  references it. Apply `dev` before `prod`.
- Backend and frontend services flap until images exist in dev's registry —
  expected on a fresh account. A production release never feeds `dev`: its images
  arrive only when you dispatch the deploy wrappers with the deploy variables
  pointed at `dev` (step 6).

Detail: [First deploy of an environment](../stack/terraform/README.md#first-deploy-of-an-environment).

### 4. Wire terraform CI to AWS (once)

1. Create the CI apply role, out of band with elevated credentials — it is
   deliberately not managed by the terraform it runs. Grant it the actions the
   `dev` root enumerates as `terraform output apply_required_actions` (the list
   already includes the `iam:SimulatePrincipalPolicy` and `iam:GetRole` the
   pre-apply guard itself needs). The `prod` root's list can only be read after
   step 5, which schedules the extension.
2. Write that role's OIDC trust policy:
   ```sh
   APPLY_ROLE_NAME=<apply-role-name> ./scripts/apply-role-trust.sh
   ```
   It trusts exactly two subjects — `ref:refs/heads/main` and
   `environment:production` — and deliberately never the `pull_request` subject.
3. Set the repository secret `AWS_ROLE_ARN` to the apply role's ARN.
4. Create the `production` GitHub Environment and give it a required reviewer. The
   release's prod terraform apply binds this environment, so that single approval
   gates every release. GitHub auto-creates the environment with no protection
   rules the first time a workflow references it — create it deliberately.

From here terraform CI is live: PRs validate offline (no plan, no credentials),
merges to `main` plan and auto-apply `dev`, and the pre-apply IAM guard fails CI
before any apply the role cannot complete, printing the one elevated apply to run.
Detail: [CI/CD roles and the pre-apply IAM guard](../stack/terraform/README.md#cicd-roles-and-the-pre-apply-iam-guard).

### 5. First apply of `prod` (human, elevated credentials)

```sh
cd stack/terraform/prod && terraform init && terraform apply
```

- On by default: RDS (pgvector), Valkey (analysis cache), Keycloak, CloudFront +
  WAF in front of an internal ALB, observability (alarms, dashboard, Slack
  forwarder).
- Off by default, enabled per-run when needed: `enable_bastion` (step 10),
  `enable_wiki_sync`, `enable_db_backup`, the ingestion worker fleets.
- The TLS certificate is requested in `us-east-1` and stays `PENDING_VALIDATION`
  until step 8 publishes its validation records.
- After the apply, extend the CI apply role's policy with the `prod` root's
  `terraform output apply_required_actions` — it differs from dev's. CI plans
  `prod` (guard included) on every push to `main` that touches `stack/terraform`,
  and the release applies it, so a missing grant turns both red.

### 6. Point the deploy engine at production

The deploy engine (`release.yml` and every `deploy-*` dispatch wrapper) reads one
set of repository variables (Settings, Secrets and variables, Actions, Variables),
so the set targets exactly one environment at a time. For the production bring-up:

- `AWS_DEPLOY_ROLE_ARN` — the **prod** root's `terraform output deploy_role_arn`
  (each root creates its own deploy role, so this exists only after step 5).
- `AWS_REGION` — `eu-west-3`.
- `DEPLOY_PROJECT` — `truth-in-stream`.
- `DEPLOY_ENVIRONMENT` — `prod`. With `dev` here, a release would apply prod
  terraform but push images to and roll the dev services.
- Optional: `DEPLOY_KEYCLOAK=false` skips the release's Keycloak job.

To roll dev images (step 3's flapping services), first fill the dev app secrets
(`make push-secrets ENV=dev` — the backend task will not stabilize without them),
then temporarily point `AWS_DEPLOY_ROLE_ARN` at the dev root's output and
`DEPLOY_ENVIRONMENT` at `dev`, dispatch `deploy-backend` and `deploy-frontend`
(dev runs no Keycloak service), and point the set back at prod afterwards. The
deploy engine fails fast, naming any variable left unset.

### 7. Fill the application secrets

Terraform creates the Secrets Manager containers empty on purpose; tasks cannot
start without values.

```sh
make push-secrets ENV=prod    # pushes the allowlisted keys from .env; asks to type "prod"
```

The allowlist lives in `scripts/push-secrets.sh`: the embedding, transcription,
LLM, and Slack keys, plus the retired legacy login keys. Leave the legacy trio
empty (you cleared the demo `AUTH_PASSWORD_HASH` in step 1) — with
`enable_legacy_password_login` off, terraform creates no containers for them and a
non-empty value would push an unmanaged secret. Three secrets are outside the
allowlist and are set by hand with `aws secretsmanager put-secret-value`:

- `truth-in-stream/prod/app/checkworthy-api-key` (crawl check-worthiness gate)
- `truth-in-stream/prod/app/factcheck-api-key` (fact-check archive producer)
- `truth-in-stream/prod/keycloak/bootstrap-admin-password`

Never set `DATABASE_URL` or `RABBITMQ_URL` by hand — terraform owns those. The
`dev` environment has its own set of containers under `truth-in-stream/dev/...`;
fill them the same way with `ENV=dev` and dev-path `put-secret-value` calls when
dev needs to run (part 2's cloud path does). Detail:
[Application secrets](../stack/terraform/README.md#application-secrets).

### 8. Publish DNS from the main account (hand-applied, CI-excluded)

The `jeminforme.fr` hosted zone lives in the main account; the app account never
writes to it. A dedicated terraform root creates the ACM validation CNAMEs and the
apex, `www`, and `login` alias records pointing at CloudFront:

```sh
cp stack/terraform/main-account/terraform.tfvars.example stack/terraform/main-account/terraform.tfvars
# fill main_account_id (and the overrides when remote state cannot be read)
make tf-main-account-plan
make tf-main-account-apply
```

Verify the certificate reaches `ISSUED` in `us-east-1` and the records resolve.
This root is deliberately excluded from CI — never add it to the terraform
workflow. Runbook: [`stack/terraform/main-account/README.md`](../stack/terraform/main-account/README.md);
background: [Cross-account ACM validation](../stack/terraform/README.md#cross-account-acm-validation).

### 9. First release

Production deploys only when a human pushes a semver tag whose commit is on `main`:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

`release.yml` then runs: guard (the tag commit must be on `main`) -> prod terraform
apply (waits for the `production` environment approval) -> backend (build, scan,
push, run migrations, roll) -> Keycloak (its DB bootstrap task runs first) and
frontend. For ad-hoc single-service rolls or a rollback, dispatch `deploy-backend`,
`deploy-frontend`, or `deploy-keycloak` from the Actions tab; `deploy-backup` only
builds and pushes the backup image (nothing rolls — the nightly task picks it up
on its next run). Detail:
[Deploys (human-gated)](infrastructure.md#deploys-human-gated).

### 10. One-time corpus load into prod RDS

After the first release the production database has schema but no corpus. Load the
locally built one (seeded claims, curated political claims, embedded evidence
chunks) through a temporary SSM bastion — `make up` locally first if you have not,
and grow the corpus beyond the offline seed via part 2's local path:

```sh
cd stack/terraform/prod && terraform apply -var enable_bastion=true
make db-tunnel ENV=prod       # keep open in its own terminal
make db-push ENV=prod         # pg_dump | pg_restore over the tunnel; text COPY keeps vectors exact
cd stack/terraform/prod && terraform apply -var enable_bastion=false
```

Detail: [Production database](infrastructure.md#production-database).

## Part 2 — start crawling and ingesting

### What the data means (read first)

A statement is only checkable if it matches the curated `claims` table (or, on the
political fast path, `political_claims`); `wiki_chunks` holds evidence used to
verify matched claims and never changes coverage. So the order of value is:
schema (migrations) -> curated claims (seed) -> evidence corpora (Wikipedia,
official statistics) -> structured lookups (parliamentary voting records). Locally,
`make up` already migrates and seeds all curated datasets offline from the
committed embedding cache.

### Choose a path

| Path | Runs on | Writes to | Use when |
|------|---------|-----------|----------|
| Cloud on-demand | Two stop/start EC2 hosts over SSM | The cloud database | Crawling at scale without keeping a machine up |
| Local pipeline | The local Docker stack | Local Postgres, then `make db-push` to prod | Bulk first builds, full control, offline seeds |

### Cloud path: on-demand ingestion hosts

One-time prerequisites (all human-gated — detail:
[Ingestion hosts, prerequisites](ingestion-hosts.md#prerequisites-human-gated-deferred-to-the-operator)):

1. `deploy/targets.json` filled with the real `dev` account id (part 1, step 1).
2. Hosts provisioned: `terraform apply -var enable_ingestion_hosts=true` in
   `stack/terraform/dev` (implies the managed database).
3. The dev secrets populated: `make push-secrets ENV=dev` covers the consumer
   host's `embedding-api-key` (clear the demo `AUTH_PASSWORD_HASH` first — part 1,
   step 1); the crawler host's two keys sit outside the allowlist and are set by
   hand — `aws secretsmanager put-secret-value` on
   `truth-in-stream/dev/app/checkworthy-api-key` and
   `truth-in-stream/dev/app/factcheck-api-key`.
4. An open SSO session (`aws sso login`). The Session Manager plugin is not
   needed here — the commands drive the hosts over `aws ssm send-command`.

Then, per source — `wikipedia`, `stats`, `factcheck`, `scrutins` (queue and
required producer env per source:
[source map](ingestion-hosts.md#source-map)):

```sh
make consumer SOURCE=wikipedia ACTION=up ENV=dev       # worker drains the queue into the DB
CRAWL_CATEGORIES="Category:Retraites en France" \
  make crawler SOURCE=wikipedia ACTION=up ENV=dev      # one-shot producer fills the queue
make consumer SOURCE=wikipedia ACTION=status ENV=dev   # watch state + backlog
make consumer SOURCE=wikipedia ACTION=down ENV=dev     # stop the host once the queue empties
```

The producer is one-shot; the consumer keeps billing until its host is stopped, so
`down` it after the drain (stopping is safe at any instant — in-flight work is
requeued). `make crawler ACTION=down ENV=dev SOURCE=...` stops the crawler host,
and the underlying commands accept `--stop-after` to do it automatically. Detail:
[Ingestion hosts](ingestion-hosts.md), cost notes:
[cost-control lifecycle](ingestion-hosts.md#cost-control-lifecycle).

### Local path: build locally, push to prod

```sh
make up                                                   # postgres + migrations + offline seed
make fleet-up                                             # broker + embedding worker fleet
make wiki-populate                                        # bulk Wikipedia corpus build (Voyage key)
make stats-ingest                                         # Eurostat + INSEE + interior ministry passages
make crawl-workers                                        # then: make crawl CRAWL_CATEGORIES="..."
make factcheck-workers                                    # then: make factcheck-crawl FACTCHECK_QUERIES="..."
make scrutins-workers                                     # then: make scrutins-crawl
```

Workers up first, then the matching producer; watch the drain at the local broker
console. When the corpus is ready, push it to production (part 1, step 10). Full
reference, diagrams, and troubleshooting:
[Ingestion pipeline](ingestion-pipeline.md).

### Recurring ingestion (optional, off by default)

- `enable_wiki_sync` — a scheduled Fargate task for a weekly wiki delta sync.
  Keep it off for now: the backend image does not yet ship the `wikisync` binary
  the task runs (the variable's description says exactly when to flip it).
- `enable_db_backup` — a nightly `pg_dump` to the private backup bucket; dispatch
  `deploy-backup` once so its image exists. Detail:
  [Production database](infrastructure.md#production-database).

Both are gated off by default and enabled with a deliberate apply; the task
mechanics live in
[`stack/terraform/modules/scheduled-task/README.md`](../stack/terraform/modules/scheduled-task/README.md).
