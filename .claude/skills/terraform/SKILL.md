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

## Pitfalls
1. Forgetting bucket versioning - native locking silently fails.
2. `use_lockfile` defaults to false - be explicit.
3. `var`/`local` in a backend block - not allowed.
4. Committing secrets in `terraform.tfvars` - use Secrets Manager / CI vars.
5. Bumping aws to a new major without reading the migration guide.
