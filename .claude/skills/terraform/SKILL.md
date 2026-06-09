---
name: terraform
description: Use when working on infrastructure in stack/terraform - AWS, directory-per-env layout, S3 native state locking, and OIDC CI auth
---

# Terraform / AWS (stack/terraform)

Terraform >= 1.11 (native S3 locking floor; latest stable is 1.15.x) · hashicorp/aws `~> 6.0` · region `eu-west-3`. Layout is **directory-per-environment**: `dev/` and `prod/` are independent root modules with isolated state; shared code lives in `modules/`.

Always run from inside an env directory: `cd stack/terraform/dev && terraform init && terraform plan`.

## Remote state
S3 bucket `truth-in-stream-tfstate` with **native S3 locking** (`use_lockfile = true`) - no DynamoDB table. Per-env state keys (`dev/terraform.tfstate`, `prod/terraform.tfstate`), `encrypt = true`.

- Bucket **versioning is mandatory** for native locking.
- The bucket must exist before `init` (bootstrap is out-of-band - see `stack/terraform/README.md`).
- `backend "s3"` blocks accept only literals - no `var`/`local` references.

## Versions / providers
Pin in `versions.tf`: `required_version = ">= 1.11.0, < 2.0.0"`, aws `version = "~> 6.0"`. AWS provider v6 has breaking changes from v5 - consult the migration guide before bumping major.

## CI auth (OIDC)
`aws-actions/configure-aws-credentials@v6` with `role-to-assume`; the job needs `permissions: id-token: write`. Set the `AWS_ROLE_ARN` repo secret. Scope the IAM role trust policy to `repo:verovec/truth-in-stream:environment:<env>`. CI (`terraform.yml`) plans on PR, applies `dev` on merge to main; prod is promoted manually.

## Environments
Each env directory holds its own `versions.tf`/`providers.tf`/`backend.tf`/`variables.tf`/`main.tf`. Prefer this over workspaces or tfvars-only - it gives true state isolation and no cross-env blast radius.

## Runtime architecture (VER-14, 2026-06; modeled on a prior internal setup)
Modules in `stack/terraform/modules/`: `vpc` (2-AZ, configurable `nat_gateway_count` — dev 1, prod 2; S3 gateway endpoint; tasks SG is egress-only), `ecs` (Fargate cluster, Container Insights; default capacity strategy is on-demand FARGATE, SPOT registered for opt-in), `ecr` (scan-on-push, keep-last-10), `iam` (account-global GitHub OIDC provider — dev creates, prod references via `create_oidc_provider=false`; deploy-role trust pinned with `StringEquals` to `repo:<org/repo>:ref:refs/heads/main`, never `:*`; task-execution role scoped to specific secret ARNs; empty app task role), `rds` (PG17 gp3 encrypted, private, generated URL-safe password -> Secrets Manager credentials + ready-made DSN secret), `alb` (HTTP + fixed-404 default; HTTPS+redirect activate when `certificate_arn` set), `service` (task def + target group + path rule + service; **adds its own per-container-port ingress rule from the ALB SG** — least-privilege, not all-ports), `migration` (one-shot golang-migrate Fargate task; deploy workflow runs it and gates on exit 0).
- Path routing: backend `/api/*`+`/healthz` (priority 10), frontend `/*` (priority 100).
- App-key secrets (`EMBEDDING_API_KEY`, `TRANSCRIPTION_API_KEY`) are created empty; values set out of band. Containers consume everything (incl. `DATABASE_URL`) as ECS secrets.
- Deploy network config (subnets/SG for `run-task`) is published via SSM `/truth-in-stream/<env>/deploy/*`.
- Two CI roles: `AWS_ROLE_ARN` (terraform plan/apply, bootstrap out-of-band) and `AWS_DEPLOY_ROLE_ARN` (narrow: ECR push, ECS deploy, run migrate task; output of the iam module).
- Pin GitHub Actions to commit SHAs, especially `aws-actions/configure-aws-credentials` and `amazon-ecr-login` (they mint/use AWS creds).
- aws provider v6: use `data.aws_region.current.region` (`.name` is deprecated). An `aws_security_group` with no `egress` block has NO egress (TF strips AWS's default allow-all) — that is intended for the Postgres SG.
- Deliberate omissions vs the reference: no customer-managed KMS (AWS-managed keys), no RabbitMQ/Redis/bastion, no cross-account Route53, dual NAT only in prod.

## Pitfalls
1. Forgetting bucket versioning - native locking silently fails.
2. `use_lockfile` defaults to false - be explicit.
3. `var`/`local` in a backend block - not allowed.
4. Committing secrets in `terraform.tfvars` - use Secrets Manager / CI vars.
5. Bumping aws to a new major without reading the migration guide.
