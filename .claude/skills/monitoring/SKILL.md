---
name: monitoring
description: Use when working on monitoring/alerts/observability in stack/terraform - CloudWatch alarms, dashboards, the *-alerts SNS topic, the treat_missing_data doctrine, and the us-east-1 WAF path.
---

# Monitoring & Alerting (stack/terraform)

This skill owns **alarms, dashboards, and SNS routing** plus the `treat_missing_data` doctrine. The Slack-forwarder Lambda runtime and packaging belong to the `lambda` skill; the wiring of these modules into `dev/`/`prod/` roots belongs to the `terraform` skill; OIDC deploy creds belong to `ci`.

Two modules, one responsibility split:

- `modules/observability` -- alarms + the `*-alerts` SNS topic(s) + the forwarder Lambda subscriber + a health `dashboard.tf`.
- `modules/monitoring` -- the **ingestion-pipeline** dashboard built on CloudWatch SEARCH expressions (custom metrics from the metrics Lambda). No alarms.

## End-to-end flow

```
AWS native metrics            custom metrics
(ALB, ECS, RDS, MQ, WAF)      (mqmetrics -> PutMetricData, namespace TruthInStream/RabbitMQ)
        |                              |
        v                              v
aws_cloudwatch_metric_alarm     aws_cloudwatch_dashboard (SEARCH widgets)
  (modules/observability)         (modules/monitoring, modules/observability/dashboard.tf)
        |
        v  alarm_actions / ok_actions
SNS topic  <project>-<env>-alerts   (WAF -> the us-east-1 copy of this topic)
        |
        v  lambda subscription
Slack-forwarder Lambda  (see the `lambda` skill)
        |
        v
Slack incoming webhook
```

The forwarder is the **single subscriber** to each `*-alerts` topic. Every regional alarm publishes to `aws_sns_topic.alerts`; the WAF alarm publishes to `aws_sns_topic.alerts_us_east_1`. A CloudWatch alarm can only act on an SNS topic **in its own region**, which is the entire reason the us-east-1 path exists (see below).

MUST: every alarm sets both `alarm_actions` and `ok_actions` to the alerts topic. `ok_actions` closes the loop in Slack so an incident shows recovery. Omitting `ok_actions` is a review-blocking defect.

## Alarms (modules/observability/main.tf)

| Alarm (resource) | Metric / namespace | Comparison | `treat_missing_data` | Notes |
|---|---|---|---|---|
| `alb_5xx` | `HTTPCode_ELB_5XX_Count` / `AWS/ApplicationELB` | `>` threshold (Sum) | `notBreaching` | LB faults, not app 5xx. Quiet LB has no datapoints; must not page. |
| `unhealthy_hosts` (`for_each` target groups) | `UnHealthyHostCount` / `AWS/ApplicationELB` | `>=` threshold (Max) | `notBreaching` | Per-service via `target_group_arn_suffixes`. Driven by `/healthz`. `evaluation_periods=3` rides out a single deregistration blip. |
| `ecs_running_tasks` (`for_each` services) | `RunningTaskCount` / `ECS/ContainerInsights` | `<` `min_running_tasks` (Avg) | **`breaching`** | A vanished service stops publishing; missing data IS the failure. `evaluation_periods=5` rides out a normal rollout dip. |
| `rds_cpu` (count) | `CPUUtilization` / `AWS/RDS` | `>` percent (Avg) | `notBreaching` | Gated on `rds_instance_id != ""`. |
| `rds_free_storage` (count) | `FreeStorageSpace` / `AWS/RDS` | `<` bytes floor (Avg) | `notBreaching` | Default floor 2 GiB. |
| `mq_cpu` (count) | `SystemCpuUtilization` / `AWS/AmazonMQ` | `>` percent (Avg) | `notBreaching` | `Broker` dimension is the broker **name**, not its id. Gated on `mq_broker_name`. |
| `waf_blocked` (count, `provider = aws.us_east_1`) | `BlockedRequests` / `AWS/WAFV2` | `>` threshold (Sum) | `notBreaching` | CLOUDFRONT scope -> us-east-1. `Rule = "ALL"`, **no `Region` dimension** (see Pitfalls). |

Optional alarms are conditional via `count = local.<x>_enabled ? 1 : 0`, where the `*_enabled` locals test the corresponding input string against `""`. An empty input disables that alarm cleanly -- this is how an env with no RDS/MQ/WAF stays quiet.

### treat_missing_data DOCTRINE (house convention)

The value encodes **what absence of data means** for that specific signal. Decide it deliberately for every new alarm; do not copy-paste.

- **`breaching`** -- absence of datapoints IS the incident. Use when a healthy resource is *guaranteed* to emit. The canonical case is `ecs_running_tasks`: a crashed/de-registered service stops publishing `RunningTaskCount`, so missing data must page. NEVER use `notBreaching` here -- it would mask total outages.
- **`notBreaching`** -- the metric is only emitted under load, so silence is normal. Use for traffic-driven counters (`alb_5xx`, `waf_blocked`) and for utilization metrics where a gap means "nothing happening" rather than "broken" (`rds_*`, `mq_cpu`, `unhealthy_hosts`).
- **`missing`** (the AWS default) -- NEVER rely on it implicitly; it leaves alarms in INSUFFICIENT_DATA limbo that neither pages nor recovers. Always set the field explicitly.
- **`ignore`** -- not used in this codebase. Avoid; it freezes alarm state and hides flapping.

## Dashboards

Two dashboards, both `aws_cloudwatch_dashboard`, both `jsonencode` a `widgets` list assembled by `concat`ing conditional widget locals so empty panels never render.

**Ingestion dashboard -- `modules/monitoring/main.tf`** (`<project>-<env>-ingestion`): the queue widgets use CloudWatch **SEARCH metric expressions** so newly-created versioned queues auto-appear with no Terraform edit:

```
SEARCH('{"TruthInStream/RabbitMQ",Broker,Queue} MetricName="Backlog"', 'Average', 60)
```

MUST double-quote a namespace containing a slash inside the SEARCH schema -- that is what `local.ns_quoted` exists for. The per-version widgets (Backlog / PublishRate / ConsumerCount) search the `Queue` dimension; the rollup widget pins the explicit `QueueBase` dimension. Worker widgets (`worker_widgets`) are omitted when `worker_service_name == ""`.

**Health dashboard -- `modules/observability/dashboard.tf`** (`<project>-<env>-ingestion-health`, gated on `create_dashboard`): static metric arrays built with `for` comprehensions over `ecs_service_names` and `target_group_arn_suffixes`, plus `rds_widgets` / `mq_widgets` / `waf_widgets` appended only when enabled. The WAF widget hard-codes `region = "us-east-1"` even though the dashboard is regional.

Metric names and dimensions on the SEARCH/custom widgets MUST match what the metrics Lambda publishes (`Backlog`, `ConsumerCount`, `PublishRate`; dimensions `Broker`, `Queue`, `QueueBase`) -- a typo silently yields an empty widget, never an error.

## us-east-1 (CloudFront / WAF)

A CLOUDFRONT-scoped WAFv2 web ACL publishes metrics **only in us-east-1**. Because an alarm can act solely on a same-region SNS topic, the WAF path is replicated there: a second `*-alerts` topic, a second copy of the forwarder Lambda (identical code/IAM), and the `waf_blocked` alarm -- all carrying `provider = aws.us_east_1` and `count = local.waf_enabled ? 1 : 0`. The `aws.us_east_1` provider alias must be passed in from the calling root (a `terraform` concern). The forwarder replication itself is documented in the `lambda` skill.

## App-side signal contract

The Go backend emits **`log/slog` JSON** to stdout only (`slog.NewJSONHandler`, `cmd/server/main.go`). There is **no Sentry, Prometheus, or Grafana** -- do not add one without an explicit decision. Logs flow to CloudWatch Logs via the ECS log driver; alarms key off CloudWatch metrics, not log parsing.

The only health contract is the `GET /healthz` route registered in `internal/handler/handler.go`, served by a `service.HealthChecker`. ECS/ALB health checks hit it; failures deregister the target, which is exactly what drives the `unhealthy_hosts` alarm and (when tasks are replaced and fail) the `ecs_running_tasks` alarm. Custom queue metrics come from the `mqmetrics` command (`cmd/mqmetrics`, `internal/mqmetrics`) calling `PutMetricData` under `METRICS_NAMESPACE` (default `TruthInStream/RabbitMQ`).

## Recipes

**Add an alarm** (in `modules/observability/main.tf`):
1. Add the resource as `aws_cloudwatch_metric_alarm`, dimensioned against an existing input (or add an input + an `*_enabled` local + `count` if it's optional).
2. Choose `treat_missing_data` per the doctrine above -- justify it in a comment.
3. Set both `alarm_actions` and `ok_actions` to `aws_sns_topic.alerts.arn` (or `alerts_us_east_1[0].arn` for a CloudFront-scoped metric, with `provider = aws.us_east_1`).
4. Add a matching threshold/period/evaluation-periods variable trio; keep defaults conservative to avoid alert spam.
5. Add a corresponding widget to `dashboard.tf` so the signal is visible, not just paged.

**Add a dashboard widget**: append a widget map to the right local list (`base_widgets`, or a `*_widgets` conditional list) with explicit `x/y/width/height`. For a dynamic per-resource set, prefer a `for` comprehension or a SEARCH expression over hand-listed metrics. For custom-metric widgets, reuse `local.ns_quoted` and the published metric/dimension names.

**Add a custom metric**: emit it from the relevant Go command via `PutMetricData` under the existing namespace with stable dimension names, then surface it in `modules/monitoring/main.tf` -- a SEARCH widget if the dimension set grows over time (e.g. versioned queues), a pinned-dimension widget for a fixed rollup.

```bash
cd stack/terraform/dev   # or prod
terraform plan -target=module.observability -target=module.monitoring
```

## Pitfalls

1. **WAF alarm INSUFFICIENT_DATA forever.** A CLOUDFRONT web ACL publishes `BlockedRequests` with only `WebACL` + `Rule`; the `Region` dimension applies to every protected-resource type EXCEPT CloudFront. CloudWatch matches an alarm to a metric by its **exact** dimension set, so adding `Region` strands the alarm. Use `WebACL` + `Rule = "ALL"` only.
2. **Wrong region for WAF/CloudFront.** The alarm, its SNS topic, and the forwarder copy must all be `provider = aws.us_east_1`. Pointing a us-east-1 alarm at the regional topic silently drops notifications.
3. **`ecs_running_tasks` with `notBreaching`.** This masks a full outage (a dead service emits no datapoints). It MUST stay `breaching`.
4. **Relying on the default `missing`.** Always set `treat_missing_data` explicitly; the default leaves alarms that neither fire nor recover.
5. **MQ `Broker` dimension.** It is the broker **name**, not the broker id. A wrong value yields an alarm in INSUFFICIENT_DATA with no error.
6. **SEARCH namespace with a slash unquoted.** `TruthInStream/RabbitMQ` must be double-quoted inside the SEARCH schema (`local.ns_quoted`); unquoted, the widget renders empty.
7. **Dashboard metric/dimension drift.** Widget metric names and dimensions must match the Lambda's published values exactly -- mismatches produce silent empty panels, never a plan error.
8. **Forgetting `ok_actions`.** An alarm without it never reports recovery in Slack; reviewers should reject it.
9. **Optional-target alarm not gated.** New RDS/MQ/WAF-style alarms must use the `*_enabled` local + `count` pattern, or envs without that resource break `terraform plan`.
