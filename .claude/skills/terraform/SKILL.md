---
name: terraform
description: Use when working on infrastructure in stack/terraform - the directory-per-env AWS layout (dev/prod/main-account roots), S3 native state locking, provider/version pinning, the module inventory, the apply-permissions IAM contract, and OIDC CI auth
---

# Terraform / AWS (stack/terraform)

Terraform >= 1.11 (native S3 locking floor; latest stable is 1.15.x) · hashicorp/aws `~> 6.0` · region `eu-west-3`. Layout is **directory-per-environment**: `dev/` and `prod/` are independent root modules with isolated state; shared code lives in `modules/`. A third root, `main-account/`, is a hand-applied DNS root (see below).

Always run from inside a root directory: `cd stack/terraform/dev && terraform init && terraform plan`.

## Remote state
S3 bucket `truth-in-stream-tfstate` with **native S3 locking** (`use_lockfile = true`) - no DynamoDB table. Per-root state keys (`dev/terraform.tfstate`, `prod/terraform.tfstate`, `main-account/terraform.tfstate`), `encrypt = true`.

- Bucket **versioning is mandatory** for native locking.
- The bucket must exist before `init` (bootstrap is out-of-band - see `scripts/bootstrap-tfstate.sh` and `stack/terraform/README.md`).
- `backend "s3"` blocks accept only literals - no `var`/`local` references.

## Versions / providers
Pin in `versions.tf`: `required_version = ">= 1.11.0, < 2.0.0"`, aws `version = "~> 6.0"`. AWS provider v6 has breaking changes from v5 - consult the migration guide before bumping major. Each root has its own pinned `.terraform.lock.hcl`.

## CI auth (OIDC)
`aws-actions/configure-aws-credentials@v6` with `role-to-assume`; the job needs `permissions: id-token: write`. Set the `AWS_ROLE_ARN` repo secret. Scope the IAM role trust policy with `StringEquals` to `repo:verovec/truth-in-stream:ref:refs/heads/main`, never `:*`. The terraform workflow (`.github/workflows/terraform.yml` -> reusable `_terraform.yml`) plans on PR, applies `dev` on merge to main; **prod is promoted manually** (`cd stack/terraform/prod && terraform init && terraform apply`). For full CI/OIDC detail -> see the **ci** skill.

## Roots
Each root holds its own `versions.tf`/`providers.tf`/`backend.tf`/`variables.tf`/`main.tf`. Prefer this over workspaces or tfvars-only - it gives true state isolation and no cross-env blast radius.

| Root | Purpose | CI |
|---|---|---|
| `dev/` | Full app stack, single NAT, gated extras off by default | plan on PR, **auto-apply** on merge to main |
| `prod/` | Full app stack, dual NAT, gated extras on | plan on PR, **manual apply** only |
| `main-account/` | DNS root in the **main account** (`<main-account-id>`, zone `jeminforme.fr`): ACM DNS-validation CNAMEs + apex/`www` CloudFront alias records | **excluded from CI** - fmt-checked only |

### `main-account/` (hand-applied, CI-excluded)
A separate root that publishes the app's public DNS into the authoritative hosted zone, which lives in the main account; the app (CloudFront + ACM cert) lives in the app account (`<app-account-id>`). It creates the ACM validation CNAMEs (so the cert moves `PENDING_VALIDATION` -> `ISSUED`) and the apex/`www` alias records pointing at CloudFront, reading those values from the prod state via `terraform_remote_state` (or pasted overrides when cross-account S3 read is not granted).

- It is **deliberately excluded** from the terraform CI workflow: that workflow lists each root it runs by explicit path and `main-account` is **not listed**, so no CI job ever plans/applies against the main account. **NEVER add it to `terraform.yml`.**
- Its only CI gate is the `main-account-terraform-fmt` job in `.github/workflows/pr.yml` - `terraform fmt -check -recursive`, no backend init, no role, no plan/apply.
- Apply order is **app account first (prod), main account second**. Runbook: `stack/terraform/main-account/README.md`.

## Module inventory (`stack/terraform/modules/`)
Networking / compute / data / edge / lambda / observability / iam / ops. Cross-cutting concerns are pushed to sibling skills: IAM authoring -> the **iam** skill; Lambda packaging/handlers -> the **lambda** skill; alarms/dashboards -> the **monitoring** skill; CI workflows -> the **ci** skill. Keep this skill focused on layout/state/versions/the module map.

| Module | Group | Purpose (one line) |
|---|---|---|
| `vpc` | networking | 2-AZ VPC, configurable `nat_gateway_count` (dev 1, prod 2), S3 gateway endpoint; tasks SG egress-only |
| `ecs` | compute | Fargate cluster, Container Insights; on-demand FARGATE default, SPOT registered for opt-in |
| `ecr` | compute | Container registry, scan-on-push, keep-last-10 lifecycle |
| `service` | compute | Load-balanced ECS service: task def + target group + listener path rule; adds its own per-port ingress from the ALB SG |
| `worker` | compute | Headless ECS consumer (no portMappings, no LB); scales by `desired_count`; outbound-only to broker/RDS/Voyage |
| `migration` | compute | One-shot golang-migrate Fargate task; deploy workflow runs it and gates on exit 0 |
| `scheduled-task` | compute | Fargate task def + optional EventBridge Scheduler schedule (+ group); empty expression = on-demand `run-task` only |
| `bastion` | compute | SSM-only EC2 (AL2023, IMDSv2 required, no public IP, egress-only); develop-locally tunnel; gated `enable_bastion` |
| `rds` | data | PG17 gp3 encrypted, private; generated URL-safe password -> Secrets Manager credentials + ready-made DSN secret |
| `valkey` | data | ElastiCache (Valkey) replication group, private subnet group; ingress from backend task SG only |
| `s3` | data | Private media bucket for direct browser upload/playback via presigned URLs; ACLs disabled, CORS-only public surface |
| `s3-backup` | data | Private bucket for pg_dump archives (embeddings); all public access blocked, no CORS |
| `rabbitmq` | data | Amazon MQ for RabbitMQ broker; inline ingress, consumers added via `allowed_security_group_ids` |
| `alb` | edge | Application Load Balancer, HTTP + fixed-404 default; HTTPS+redirect activate when `certificate_arn` set |
| `acm` | edge | Public TLS cert for CloudFront, requested in **us-east-1**, DNS validation; does NOT create records or wait (records live in main-account root) |
| `cloudfront` | edge | CloudFront distribution + VPC origin (PrivateLink to the internal ALB); CachingDisabled, AllViewer forwarding |
| `waf` | edge | CLOUDFRONT-scoped WAFv2 web ACL, default-allow, managed groups + rate-based rule, decision logging |
| `metrics-lambda` | lambda | Metrics-poller Lambda (VPC-attached, scoped secret read, namespaced metric publish) on an EventBridge schedule |
| `observability` | observability | Regional alerts SNS topic, CloudWatch alarms (ALB 5xx, unhealthy targets, ECS/RDS/MQ/WAF), Slack forwarder Lambda, health dashboard |
| `monitoring` | observability | CloudWatch dashboard with SEARCH-metric widgets that auto-discover versioned queues (no dashboard edit per queue) |
| `iam` | iam | Account-global GitHub OIDC provider + deploy/task-execution/app roles and policies (-> the **iam** skill) |
| `apply-permissions` | iam | Declarative manifest of apply-time AWS actions the CI apply role must hold; enforced by `iam-apply-guard.sh` (see below) |

## apply-permissions contract
`stack/terraform/modules/apply-permissions/main.tf` is a **declarative manifest** of every apply-time AWS action the CI apply role (`AWS_ROLE_ARN`) must hold to provision an environment. It exists because the apply role cannot grant itself permissions it lacks - the first apply introducing a new resource type would fail halfway. The module aggregates per-concern action blocks (state, networking, ecs, ecr, alb, iam, logs, ssm, secrets, s3, mq, and gated areas: acm/cloudfront/waf/rds/valkey/scheduled-tasks/bastion/metrics-lambda/observability) into the `apply_required_actions` output (sorted+deduped).

- **Maintenance contract:** when a card adds a resource area, append its actions to the matching block **in the same change**, so the required permissions never drift from what terraform provisions. List concrete actions only - **NEVER `"*"`**. This is the action contract; resource-level scoping lives on the role's own policy (the **iam** skill).
- Enumerate the **full** action set the provider needs (create/read/update/delete/list/tag), not just create/delete - the provider refreshes state on every plan, so missing read/describe actions 403 on refresh. (Mirrors the repo IAM completeness rule.)

`scripts/iam-apply-guard.sh <plan-json> <apply-role-arn> [env-dir]` runs in CI **before every `terraform apply`** (`_terraform.yml`): it reads `apply_required_actions` from `terraform show -json`, batches them (cap 100) through `aws iam simulate-principal-policy` against the apply role, and **fails closed** if any action is denied or the response can't be parsed - printing the missing actions and the one manual apply an operator must run with elevated credentials. The guard itself needs `iam:SimulatePrincipalPolicy` + `iam:GetRole` on the apply role.

## Runtime architecture (VER-14 / VER-62)
- Path routing: backend `/api/*`+`/healthz` (priority 10), frontend `/*` (priority 100).
- App-key secrets (`EMBEDDING_API_KEY`, `TRANSCRIPTION_API_KEY`) are created empty; values set out of band. Containers consume everything (incl. `DATABASE_URL`) as ECS secrets.
- Deploy network config (subnets/SG for `run-task`) is published via SSM `/truth-in-stream/<env>/deploy/*`.
- Two CI roles: `AWS_ROLE_ARN` (terraform plan/apply, bootstrap out-of-band) and `AWS_DEPLOY_ROLE_ARN` (narrow: ECR push, ECS deploy, run migrate task; output of the `iam` module).
- Pin GitHub Actions to commit SHAs, especially `configure-aws-credentials` and `amazon-ecr-login` (they mint/use AWS creds).
- aws provider v6: use `data.aws_region.current.region` (`.name` is deprecated). An `aws_security_group` with no `egress` block has NO egress (TF strips AWS's default allow-all) - intended for the Postgres SG.
- `bastion` (VER-62): the develop-locally tunnel. `scripts/ssm-port-forward.sh` opens `AWS-StartPortForwardingSessionToRemoteHost` to the private broker so the worker drains the cloud queue into the LOCAL Postgres. Reach the broker by adding the bastion SG to the rabbitmq module's `allowed_security_group_ids` (reuses the broker's inline ingress; a separate rule on an inline-ruled SG conflicts, and an egress-to-broker rule would cycle).
- Deliberate omissions vs the reference: no customer-managed KMS (AWS-managed keys), no cross-account Route53 inside the app roots, dual NAT only in prod.

## Pitfalls
1. Forgetting bucket versioning - native locking silently fails.
2. `use_lockfile` defaults to false - be explicit.
3. `var`/`local` in a backend block - not allowed.
4. Committing secrets in `terraform.tfvars` - use Secrets Manager / CI vars.
5. Bumping aws to a new major without reading the migration guide.
6. Adding `main-account/` to `terraform.yml` - it is hand-applied and CI-excluded; only `pr.yml`'s `main-account-terraform-fmt` job touches it.
7. Adding a new resource area without appending its actions to `modules/apply-permissions` - the next `terraform apply` half-fails and the guard blocks CI.
8. Listing only create/delete in apply-permissions - the provider refreshes on every plan, so missing read/describe/list/tag actions 403; enumerate the full lifecycle.
9. ACM cert in the wrong region - CloudFront certs MUST be in us-east-1; the `acm` module does not create validation records (the main-account root does).
