---
name: lambda
description: Use when adding, packaging, or wiring AWS Lambdas in stack/terraform - prebuilt-artifact zips, invocation triggers (EventBridge Scheduler, SNS, direct invoke), VPC attachment, scoped IAM, and the us-east-1 forwarder replica
---

# Lambda (stack/terraform)

Three Lambda-bearing modules live under `stack/terraform/modules/`: `metrics-lambda`, `observability` (the Slack forwarder), and `worker-lifecycle`. Every Lambda here packages a **prebuilt artifact** via an `archive_file` data source - Terraform never builds code. Go functions are `provided.al2023` on `arm64`; the one Python function is `python3.13` (x86 default). See `terraform` for the directory-per-env layout, `iam` for the policy-document conventions these modules follow, `monitoring`/`go` for the alarm and Go-build context, and `ci` for how artifacts reach an apply.

## Hard rules

- ALWAYS package via `data "archive_file"` over a prebuilt file. NEVER add a `null_resource`/`local-exec` build step or `filename` pointing at a hand-zipped blob. Go binaries are built out-of-band (`make lambda-mqmetrics`, `make lambda-workerlifecycle` in `stack/backend`); the Python forwarder is a single source file zipped in place.
- ALWAYS wire `source_code_hash = data.archive_file.<x>.output_base64sha256`. NEVER omit it - without it Terraform cannot tell a new artifact from the old one and silently skips the redeploy.
- For Go: `runtime = "provided.al2023"`, `architectures = ["arm64"]`, `handler = "bootstrap"`, and the binary MUST be named `bootstrap` at the zip root (`source_file` preserves the name). NEVER use `source_dir` for a Go function - it would nest the binary under a path and break the runtime contract.
- For Python: rely on the runtime's bundled `boto3`; NEVER vendor it. Use only the stdlib otherwise (the forwarder uses `urllib`, not `requests`) so there is no build step. If you need a third-party dep, that is a layer/build decision - stop and reconsider.
- Every module OWNS its `aws_cloudwatch_log_group` with finite `retention_in_days`. NEVER let Lambda auto-create the group - an auto-created group lingers with never-expire retention and survives `destroy`.
- The Lambda MUST `depends_on` both the log group and the role-policy attachment. NEVER let the function race ahead of its IAM or log group.
- IAM is authored as `aws_iam_policy_document` data sources, scoped to least privilege - see `iam`. NEVER inline raw JSON or attach broad managed policies beyond the two service-roles below.
- VPC-attach (`vpc_config`) any function that reaches the in-VPC RabbitMQ management API or RDS, and attach `AWSLambdaVPCAccessExecutionRole` for the ENI lifecycle. The forwarder is NOT in a VPC (it only calls AWS APIs + an outbound webhook).
- `cloudwatch:PutMetricData` has no resource-level scoping; the `cloudwatch:namespace` `StringEquals` condition is what keeps it least-privilege. NEVER ship `PutMetricData` on `*` without the namespace condition.

## Inventory

| Module | Runtime / arch | Handler | Source | VPC | Trigger | Notes |
|---|---|---|---|---|---|---|
| `metrics-lambda` | `provided.al2023` / arm64 | `bootstrap` | prebuilt Go binary (`source_binary_path`, `make lambda-mqmetrics`) | yes | EventBridge Scheduler | Polls RabbitMQ management API, emits namespaced `PutMetricData`; `maximum_retry_attempts = 0` |
| `observability` (forwarder) | `python3.13` / x86 | `handler.handler` | `forwarder/handler.py`, no build | no | SNS (`aws_lambda_permission` + subscription) | Forwards CloudWatch-alarm SNS to Slack; replicated into us-east-1 for WAF |
| `worker-lifecycle` | `provided.al2023` / arm64 | `bootstrap` | one prebuilt Go binary (`source_binary_path`, `make lambda-workerlifecycle`) | yes | scale/cleanup <- Scheduler, deploy <- direct invoke | One binary, three functions selected by `LIFECYCLE_HANDLER` env; the EXTERNAL ECS deployment controller |

### metrics-lambda

Single Go function. The execution role attaches `AWSLambdaVPCAccessExecutionRole` (ENI + Logs) plus an inline policy: scoped `secretsmanager:GetSecretValue` on the broker-URL secret and `cloudwatch:PutMetricData` conditioned on `cloudwatch:namespace == var.metrics_namespace`. Invoked by an **EventBridge Scheduler** (`aws_scheduler_schedule`), not EventBridge Rules. A dedicated `aws_scheduler_schedule_group` exists so the scheduler role's trust can be pinned with `aws:SourceArn = group.arn` (per-schedule scoping is unsupported) - keep that group even for one schedule. `flexible_time_window { mode = "OFF" }` and `retry_policy { maximum_retry_attempts = 0 }`: a metrics poll is fire-and-forget, a missed tick is superseded by the next, and Scheduler bypasses Lambda's async-invoke retries.

### observability (Slack forwarder)

Python `forwarder/handler.py` zipped in place; `format_slack_message` is a pure, unit-tested function (see `forwarder/handler_test.py`). The execution role attaches `AWSLambdaBasicExecutionRole` plus a one-statement inline policy reading only the webhook secret. Triggering is two resources: an `aws_lambda_permission` with `principal = "sns.amazonaws.com"` and `source_arn = topic.arn`, and an `aws_sns_topic_subscription` that `depends_on` the permission (the subscription fails if the permission is not yet in place). The webhook URL is read from Secrets Manager at runtime and cached across warm invocations - NEVER bake it into env/code. This module also owns the alarm/dashboard fleet - alarm specifics belong to `monitoring`.

### worker-lifecycle

One zipped `bootstrap` binary backs THREE `aws_lambda_function` resources (`for_each` over a `functions` map); `LIFECYCLE_HANDLER` (`scale` / `cleanup` / `deploy`) selects the behavior at runtime. `scale` and `cleanup` are scheduled (Scheduler, same group + role pattern, `maximum_retry_attempts = 0`) and read queue depth; `deploy` has an empty schedule and is invoked directly by `scripts/deploy-ingestion.sh`. It is the **EXTERNAL ECS deployment controller**: it registers task-def revisions, manages task sets, drains in-flight work, and promotes the primary task set. The per-service scaling policy lives in an `aws_ssm_parameter` (read at cold start) rather than env, because the full map can exceed the 4 KiB env limit. The inline IAM policy scopes ECS service/task/task-set actions to the one cluster, keeps `RegisterTaskDefinition`/`DescribeTaskDefinition` on `*` (the family carries no cluster condition key), and `iam:PassRole` is constrained to the worker task + execution roles with `iam:PassedToService = ecs-tasks.amazonaws.com`.

## Packaging conventions

- `data "archive_file" "<x>"` with `type = "zip"`; Go uses `source_file = var.source_binary_path`, Python uses `source_file = "${path.module}/forwarder/handler.py"`. `output_path` sits next to the source.
- `source_code_hash` from `output_base64sha256` on the same archive - the redeploy trigger.
- One `aws_cloudwatch_log_group` per function named `/aws/lambda/<function_name>` with `var.log_retention_days`. `worker-lifecycle` `for_each`es the group over its functions.
- `depends_on = [<role-policy-attachment>, <log-group>]` on every function.
- Go is arm64 (cheaper, faster cold starts here); Python takes the x86 default.

## Invocation patterns

| Lambda | Invoked by | Mechanism |
|---|---|---|
| `metrics-lambda` | EventBridge Scheduler | `aws_scheduler_schedule` -> scoped scheduler role (`lambda:InvokeFunction` on the function ARN), no retries |
| forwarder | SNS | `aws_lambda_permission` (SNS principal) + `aws_sns_topic_subscription` |
| `worker-lifecycle` scale/cleanup | EventBridge Scheduler | same Scheduler + scoped-role pattern, no retries |
| `worker-lifecycle` deploy | direct invoke (external controller) | `aws lambda invoke` from `scripts/deploy-ingestion.sh` with a JSON `{image, services}` payload |

The deploy invoke uses `--cli-binary-format raw-in-base64-out`; `aws lambda invoke` exits 0 even on a function error, so the script checks the response for `FunctionError`. A missing function (`ResourceNotFoundException`) is treated as skip, not fatal, so a deploy can run before the pipeline is fully stood up. The default function name there is `<project>-<environment>-workerlifecycle-deploy` - keep module naming consistent with that contract.

## IAM

Author every policy as an `aws_iam_policy_document` data source rendered into an `aws_iam_role_policy` (inline) - see `iam` for the full conventions. The two service-role attachments allowed here: `AWSLambdaVPCAccessExecutionRole` (VPC functions) and `AWSLambdaBasicExecutionRole` (non-VPC). Beyond those, grant only the scoped statements the handler actually uses: namespaced `PutMetricData`, `GetSecretValue` on the specific secret ARN, `ssm:GetParameter` on the specific parameter, the cluster-scoped ECS set, and the conditioned `PassRole`. The Scheduler role is a separate role whose trust is pinned to the schedule-group ARN and whose only action is `lambda:InvokeFunction` on the target function ARN(s).

## Pitfalls

1. **us-east-1 replication for CloudFront/WAF.** WAFV2 metrics for a CLOUDFRONT-scoped web ACL are published ONLY in us-east-1, and a CloudWatch alarm can only target an SNS topic in its own region. So `observability` stands up a full second copy in us-east-1 (`*_us_east_1` resources with `provider = aws.us_east_1`): topic, role, policy, log group, function, permission, subscription - all `count = local.waf_enabled ? 1 : 0`. The forwarder code and IAM are identical; the archive is shared. If you add a CloudFront/WAF alarm, route it to the us-east-1 topic, not the regional one, or it silently never alerts.
2. **One binary, three handlers (worker-lifecycle).** The same zip backs `scale`, `cleanup`, and `deploy`; behavior is chosen at runtime by `LIFECYCLE_HANDLER`. NEVER assume a per-handler artifact. When you add a fourth behavior, add an entry to the `functions` map (with its env + schedule) - do not build a separate binary. A non-empty `schedule` is what marks a function as scheduled (`scheduled_functions` filter); leave it `""` for a direct-invoke handler.
3. **Forgetting `source_code_hash`.** Without it a rebuilt artifact does not redeploy. The hash MUST come from the archive's `output_base64sha256`, not from the source file.
4. **Auto-created log group.** Omitting the `aws_cloudwatch_log_group` lets Lambda create one with infinite retention that outlives `destroy`. Always declare it and add it to `depends_on`.
5. **Wrong Go zip layout.** `provided.al2023` requires a root-level executable named `bootstrap`. Using `source_dir`, a differently named binary, or x86 when the build targets arm64 yields a Runtime.InvalidEntrypoint at invoke time, not at apply.
6. **SNS subscription before permission.** The subscription must `depends_on` the `aws_lambda_permission`; created first it fails because SNS cannot yet invoke the function.
7. **Scheduler trust scoping.** EventBridge Scheduler cannot scope a role's trust to an individual schedule - only to a schedule group. Keep the dedicated `aws_scheduler_schedule_group` and pin `aws:SourceArn` to it; reusing the account default group widens the trust boundary.
8. **`PutMetricData` namespace condition.** It is the only thing scoping that action (no resource ARNs exist for it). Dropping the `cloudwatch:namespace` condition grants account-wide metric writes.
