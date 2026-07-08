---
name: iam
description: Use when authoring or reviewing roles, trust policies, deploy permissions, or the apply-permissions manifest while working on IAM in stack/terraform; covers the policy-document house pattern, GitHub OIDC trust, least-privilege deploy scoping, the task-role layering, and the CI apply guard contract.
---

# IAM (stack/terraform)

The IAM module (`stack/terraform/modules/iam/`) provisions the GitHub OIDC provider, the GitHub-Actions deploy role, and the two ECS task roles. The action contract the CI apply role must itself hold lives in a separate module (`stack/terraform/modules/apply-permissions/`) enforced by a pre-apply guard (`scripts/iam-apply-guard.sh`). Read this before touching any role, policy, or trust relationship. Cross-references: `terraform` (module wiring, env roots), `ci` (where the guard runs in `_terraform.yml`), `lambda` (functions whose execution roles and resource policies flow through these manifests).

## Authoring style — MUST

Author every policy as a `data "aws_iam_policy_document"` block and render it with `.json` into the role resource. This is the house pattern across the stack (iam, metrics-lambda, observability all use policy-document data sources). NEVER hand-write `jsonencode({...})` policy literals, and NEVER inline a heredoc JSON string.

```hcl
data "aws_iam_policy_document" "task_media" {
  statement {
    sid       = "MediaObjects"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["${var.media_bucket_arn}/*"]
  }
}

resource "aws_iam_role_policy" "task_media" {
  name   = "media-storage"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_media.json
}
```

Rules: every `statement` carries a `sid`; conditions use a `condition { test / variable / values }` block, never a JSON `Condition` map; optional policies are gated with `count` on the data source AND the `aws_iam_role_policy` (see `task_media`, `task_db_backup` in `modules/iam/main.tf`); trust policies are policy documents too (`deploy_trust`, `ecs_tasks_trust`) rendered into `assume_role_policy`.

## GitHub OIDC — MUST

The OIDC provider is account-global. Create it once (dev) and reference it everywhere else. Drive this with the `create_oidc_provider` flag: `true` creates `aws_iam_openid_connect_provider.github` (count 1), `false` reads it via the `data` source (count 1); `local.oidc_provider_arn` selects whichever exists. NEVER create the provider in two environments — the apply will collide on the account-global resource.

Deploy-role trust MUST pin the subject to one ref. NEVER use `:*` or a wildcard `sub` — that lets any branch, PR, or fork assume the deploy role.

```hcl
condition {
  test     = "StringEquals"
  variable = "token.actions.githubusercontent.com:aud"
  values   = ["sts.amazonaws.com"]
}
condition {
  test     = "StringEquals"
  variable = "token.actions.githubusercontent.com:sub"
  values   = ["repo:${var.github_repository}:ref:${var.github_deploy_ref}"]
}
```

Both the `aud` and `sub` `StringEquals` conditions are required; the federated principal is `local.oidc_provider_arn`; the action is `sts:AssumeRoleWithWebIdentity`.

## Least-privilege deploy policy — MUST

`data.aws_iam_policy_document.deploy` is the deploy role's only policy. Each statement is scoped as tightly as AWS allows. When you add a deploy-time capability, follow these scoping rules exactly.

| sid | actions | resource scoping |
|---|---|---|
| `EcrAuth` | `ecr:GetAuthorizationToken` | `*` (the only resource AWS accepts for this action) |
| `EcrPush` | `ecr:BatchCheckLayerAvailability`, `BatchGetImage`, `GetDownloadUrlForLayer`, `InitiateLayerUpload`, `UploadLayerPart`, `CompleteLayerUpload`, `PutImage` | `var.ecr_repository_arns` (never `*`) |
| `EcsDeploy` | `ecs:DescribeServices`, `UpdateService`, `RunTask`, `DescribeTasks`, `DescribeTaskDefinition` | `*` gated by `ArnEquals` on `ecs:cluster` = `var.cluster_arn` |
| `EcsTaskDefinition` | `ecs:DescribeTaskDefinition`, `RunTask` | `task-definition/${local.name}-*` (family carries no cluster condition key) |
| `EcsRegisterTaskDefinition` | `ecs:RegisterTaskDefinition` | `*` — AWS offers no resource-level ARN or family/cluster condition key; document this in-code |
| `InvokeEnvironmentLambda` | `lambda:InvokeFunction` | `function:${local.name}-*` (this env's functions only; currently unused, retained after the Fargate worker-fleet roll was retired) |
| `PassTaskRoles` | `iam:PassRole` | the two task-role ARNs, gated by `iam:PassedToService` = `ecs-tasks.amazonaws.com` |
| `ReadDeployParameters` | `ssm:GetParameter`, `GetParameters` | `var.ssm_parameter_arns`, in a `dynamic` block keyed on a non-empty list |

The `*` resources on `EcsRegisterTaskDefinition` and `EcsDeploy` are the only intentional wildcards and are bounded — `RegisterTaskDefinition` has no scoping mechanism (keep the explanatory comment), and `EcsDeploy`'s blast radius is fenced by the cluster condition. `iam:PassRole` MUST always carry the `iam:PassedToService` condition; an unconditioned `PassRole` is a privilege-escalation hole. Keep the in-code comments that explain the `*` choices — they are the audit trail.

## Role layers — MUST

Three roles, three jobs. NEVER collapse them.

| Role | Trust | Policies | Purpose |
|---|---|---|---|
| `deploy` (`${name}-deploy`) | GitHub OIDC web identity, ref-pinned | `data.aws_iam_policy_document.deploy` | what GitHub Actions may do at deploy time |
| `task_execution` (`${name}-task-execution`) | `ecs-tasks.amazonaws.com` | managed `AmazonECSTaskExecutionRolePolicy` + scoped `secretsmanager:GetSecretValue` on `var.secret_arns` | image pull, log writes, secret injection at task start |
| `task` (`${name}-task`) | `ecs-tasks.amazonaws.com` | near-empty; conditionally media-S3 (`GetObject`/`PutObject`/`ListBucket`) and write-only db-backup (`PutObject`) | what the running application itself may call |

The execution role gets the managed policy plus secrets-injection only — NEVER attach application permissions here. Application AWS access goes on the `task` role. The `task` role starts empty; the media policy (object-level `GetObject`/`PutObject` on `${bucket}/*` plus bucket-level `ListBucket` so a missing-key HEAD returns 404 not 403) and the backup policy (`PutObject` only — the scheduled backup task assumes this same role, so it may upload dumps but never read or delete them) attach only when their bucket ARN var is non-empty. Both task roles share `ecs_tasks_trust`.

## The apply-permissions contract — distinctive, MUST

`modules/apply-permissions/` is the declarative single source of truth for the apply-time actions the CI apply role (`AWS_ROLE_ARN`) must hold to provision an environment. It emits one `actions` output (sorted, deduped), surfaced by each env root as `apply_required_actions`. The apply role cannot grant itself permissions it lacks, so an apply introducing a new resource type would otherwise fail halfway. The guard catches that up front.

`scripts/iam-apply-guard.sh` runs on every plan with credentials in `ci` (`.github/workflows/_terraform.yml`, after `terraform plan`, on both PR and main) — so a missing permission is caught before merge, not at apply. It reads `apply_required_actions` from `terraform show -json`, batches them (100 at a time, the `simulate-principal-policy` cap), and runs `aws iam simulate-principal-policy` against the apply role. Any non-`allowed` decision fails the job and prints the missing actions plus the one manual `terraform apply` to run with elevated credentials. It fails closed: a parse error or a missing `iam:SimulatePrincipalPolicy` aborts rather than passing silently.

Workflow when you add a resource needing a new apply-time action:

1. Add the resource in its module (`terraform`).
2. Append the action(s) to the matching `*_actions` block in `modules/apply-permissions/main.tf` — or add a new gated block and fold it into the `concat` in `_actions`, keyed on the right `include_*` flag.
3. List concrete actions only; NEVER `"*"`. Resource-level scoping is enforced on each role's own policy — this manifest is purely the action contract.
4. If the action is genuinely new to the role, that first apply must be run once manually with elevated credentials (the guard tells the operator exactly this); subsequent CI applies pass.

The guard itself needs `iam:SimulatePrincipalPolicy` and `iam:GetRole` (the `guard_actions` block). NEVER remove an action from the manifest just to make the guard green — that re-opens the half-apply hole.

## Permission-completeness doctrine — MUST

Mirrors the project-wide IAM rule: when a module starts using a new AWS service, enumerate the FULL resource lifecycle the Terraform provider exercises, not just create/delete. The provider refreshes state on every plan, so read actions are not optional — a missing read 403s on refresh.

For each new resource type cover: create, read/refresh (`Get*`/`Describe*`), update, delete, list, tagging (`TagResource`/`UntagResource`/`ListTagsForResource`), and implicit dependencies. The existing blocks model this — e.g. `cloudfront_actions` and `waf_actions` include `TagResource`/`UntagResource`/`ListTags*` because the provider's transparent tag interceptor reads and writes tags on every plan; `elasticache_actions` includes `DescribeCacheParameterGroups`/`DescribeEngineDefaultParameters` for drift detection; `waf_actions` folds in `logs:PutResourcePolicy` for the decision-log resource policy; `observability_actions` includes `lambda:AddPermission`/`RemovePermission` for the SNS-invoke resource policy. Implicit deps for result/output buckets (S3 write for anything dumping artifacts) MUST be included alongside the service's own API actions.

## Pitfalls

1. Hand-writing `jsonencode` or heredoc policy JSON instead of a `aws_iam_policy_document` data source — breaks the house pattern; always use the data source rendered via `.json`.
2. A `sub` trust condition with `:*` or any wildcard — lets PRs and forks assume the deploy role. Pin to `repo:${repo}:ref:${ref}` with `StringEquals`.
3. Creating the OIDC provider in more than one environment — it is account-global; only the env with `create_oidc_provider = true` creates it, the rest read it.
4. `iam:PassRole` without the `iam:PassedToService = ecs-tasks.amazonaws.com` condition — privilege escalation; the condition is mandatory.
5. Attaching application permissions to `task_execution` instead of `task` — the execution role is for pull/logs/secret-injection only.
6. Adding a resource without updating `modules/apply-permissions/main.tf` — the guard fails the plan in CI. Update the matching `*_actions` block (or a new gated block + `concat` entry) in the same change.
7. Listing `"*"` in the apply-permissions manifest — forbidden; list concrete actions only. Resource scoping belongs on the role policy, not the action contract.
8. Omitting read/refresh or tagging actions for a new service in the manifest — the provider 403s on plan refresh even when create succeeded.
9. Forgetting a service's implicit deps (e.g. an output/result S3 bucket, a log resource policy, a lambda resource policy) — enumerate them with the service's own actions.
10. Removing an action from the manifest to silence the guard — re-opens the half-apply hole the guard exists to prevent.
