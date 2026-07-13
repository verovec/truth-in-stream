data "aws_region" "current" {}

locals {
  name   = "${var.project}-${var.environment}"
  region = data.aws_region.current.region

  rds_enabled = var.rds_instance_id != ""
  mq_enabled  = var.mq_broker_name != ""
  waf_enabled = var.waf_web_acl_name != ""

  # Queue alarms need the metrics-lambda namespace, the broker name (the Broker
  # dimension value), and at least one base queue to key on. Run alarms need the
  # run-outcome namespace and at least one source. Either set empty disables its
  # alarms cleanly (an env without the metrics lambda stays quiet).
  queue_alarms_enabled = var.queue_metrics_namespace != "" && var.mq_broker_name != "" && length(var.queue_bases) > 0
  run_alarms_enabled   = var.run_metrics_namespace != "" && length(var.run_sources) > 0
}

# ---------------------------------------------------------------------------
# Alerts SNS topic (regional). Every regional alarm publishes here, and the
# forwarder Lambda is the single subscriber.
# ---------------------------------------------------------------------------
resource "aws_sns_topic" "alerts" {
  name = "${local.name}-alerts"
}

# ---------------------------------------------------------------------------
# Slack forwarder Lambda. Reads the webhook from Secrets Manager at runtime and
# posts each alarm to Slack. Single source file, no build step: archive_file
# zips handler.py and the python3.13 runtime bundles boto3.
# ---------------------------------------------------------------------------
data "aws_iam_policy_document" "lambda_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "forwarder" {
  name               = "${local.name}-slack-forwarder"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "forwarder_basic" {
  role       = aws_iam_role.forwarder.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# Least privilege: read only the one webhook secret, nothing else.
data "aws_iam_policy_document" "forwarder" {
  statement {
    sid       = "ReadSlackWebhook"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.slack_webhook_secret_arn]
  }
}

resource "aws_iam_role_policy" "forwarder" {
  name   = "read-webhook"
  role   = aws_iam_role.forwarder.id
  policy = data.aws_iam_policy_document.forwarder.json
}

data "archive_file" "forwarder" {
  type        = "zip"
  source_file = "${path.module}/forwarder/handler.py"
  output_path = "${path.module}/forwarder/handler.zip"
}

# Own the log group so retention is finite and a destroy removes it (an
# auto-created group lingers with never-expire retention).
resource "aws_cloudwatch_log_group" "forwarder" {
  name              = "/aws/lambda/${local.name}-slack-forwarder"
  retention_in_days = var.log_retention_days
}

resource "aws_lambda_function" "forwarder" {
  function_name = "${local.name}-slack-forwarder"
  description   = "Forwards CloudWatch alarm SNS notifications to the Slack incoming webhook."

  filename         = data.archive_file.forwarder.output_path
  source_code_hash = data.archive_file.forwarder.output_base64sha256

  runtime = "python3.13"
  handler = "handler.handler"
  role    = aws_iam_role.forwarder.arn
  timeout = 15

  environment {
    variables = {
      SLACK_WEBHOOK_SECRET_ARN = var.slack_webhook_secret_arn
    }
  }

  depends_on = [
    aws_iam_role_policy_attachment.forwarder_basic,
    aws_cloudwatch_log_group.forwarder,
  ]
}

resource "aws_lambda_permission" "forwarder_from_sns" {
  statement_id  = "AllowExecutionFromSNS"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.forwarder.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.alerts.arn
}

resource "aws_sns_topic_subscription" "forwarder" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.forwarder.arn

  depends_on = [aws_lambda_permission.forwarder_from_sns]
}

# ---------------------------------------------------------------------------
# us-east-1 alerts path for the CLOUDFRONT-scoped WAF. WAFV2 metrics for a
# CLOUDFRONT web ACL are published in us-east-1, so the alarm, its SNS topic, and
# a second copy of the forwarder all live there (a CloudWatch alarm can only act
# on an SNS topic in its own region). The forwarder code and IAM are identical.
# ---------------------------------------------------------------------------
resource "aws_sns_topic" "alerts_us_east_1" {
  count    = local.waf_enabled ? 1 : 0
  provider = aws.us_east_1
  name     = "${local.name}-alerts"
}

resource "aws_iam_role" "forwarder_us_east_1" {
  count              = local.waf_enabled ? 1 : 0
  provider           = aws.us_east_1
  name               = "${local.name}-slack-forwarder-use1"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "forwarder_us_east_1_basic" {
  count      = local.waf_enabled ? 1 : 0
  provider   = aws.us_east_1
  role       = aws_iam_role.forwarder_us_east_1[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy" "forwarder_us_east_1" {
  count    = local.waf_enabled ? 1 : 0
  provider = aws.us_east_1
  name     = "read-webhook"
  role     = aws_iam_role.forwarder_us_east_1[0].id
  policy   = data.aws_iam_policy_document.forwarder.json
}

resource "aws_cloudwatch_log_group" "forwarder_us_east_1" {
  count             = local.waf_enabled ? 1 : 0
  provider          = aws.us_east_1
  name              = "/aws/lambda/${local.name}-slack-forwarder"
  retention_in_days = var.log_retention_days
}

resource "aws_lambda_function" "forwarder_us_east_1" {
  count    = local.waf_enabled ? 1 : 0
  provider = aws.us_east_1

  function_name = "${local.name}-slack-forwarder"
  description   = "Forwards CLOUDFRONT WAF alarm SNS notifications to the Slack incoming webhook."

  filename         = data.archive_file.forwarder.output_path
  source_code_hash = data.archive_file.forwarder.output_base64sha256

  runtime = "python3.13"
  handler = "handler.handler"
  role    = aws_iam_role.forwarder_us_east_1[0].arn
  timeout = 15

  environment {
    variables = {
      SLACK_WEBHOOK_SECRET_ARN = var.slack_webhook_secret_arn
    }
  }

  depends_on = [
    aws_iam_role_policy_attachment.forwarder_us_east_1_basic,
    aws_cloudwatch_log_group.forwarder_us_east_1,
  ]
}

resource "aws_lambda_permission" "forwarder_us_east_1_from_sns" {
  count         = local.waf_enabled ? 1 : 0
  provider      = aws.us_east_1
  statement_id  = "AllowExecutionFromSNS"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.forwarder_us_east_1[0].function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.alerts_us_east_1[0].arn
}

resource "aws_sns_topic_subscription" "forwarder_us_east_1" {
  count     = local.waf_enabled ? 1 : 0
  provider  = aws.us_east_1
  topic_arn = aws_sns_topic.alerts_us_east_1[0].arn
  protocol  = "lambda"
  endpoint  = aws_lambda_function.forwarder_us_east_1[0].arn

  depends_on = [aws_lambda_permission.forwarder_us_east_1_from_sns]
}

# ---------------------------------------------------------------------------
# Alarms. Every alarm routes to the regional alerts topic (WAF to the us-east-1
# topic). actions_enabled is on; ok_actions close the loop in Slack.
# ---------------------------------------------------------------------------

# ALB load-balancer 5xx faults. Treat-missing-data as notBreaching so a quiet
# load balancer (no traffic, no datapoints) does not page.
resource "aws_cloudwatch_metric_alarm" "alb_5xx" {
  alarm_name          = "${local.name}-alb-5xx"
  alarm_description   = "ALB-generated 5xx responses exceeded the threshold."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.alb_5xx_evaluation_periods
  metric_name         = "HTTPCode_ELB_5XX_Count"
  namespace           = "AWS/ApplicationELB"
  period              = var.alb_5xx_period_seconds
  statistic           = "Sum"
  threshold           = var.alb_5xx_threshold
  treat_missing_data  = "notBreaching"

  dimensions = {
    LoadBalancer = var.alb_arn_suffix
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# Unhealthy targets per service target group.
resource "aws_cloudwatch_metric_alarm" "unhealthy_hosts" {
  for_each = var.target_group_arn_suffixes

  alarm_name          = "${local.name}-${each.key}-unhealthy-hosts"
  alarm_description   = "${each.key} has unhealthy targets in its target group."
  comparison_operator = "GreaterThanOrEqualToThreshold"
  evaluation_periods  = var.unhealthy_host_evaluation_periods
  metric_name         = "UnHealthyHostCount"
  namespace           = "AWS/ApplicationELB"
  period              = var.unhealthy_host_period_seconds
  statistic           = "Maximum"
  threshold           = var.unhealthy_host_threshold
  treat_missing_data  = "notBreaching"

  dimensions = {
    LoadBalancer = var.alb_arn_suffix
    TargetGroup  = each.value
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# ECS running-task drop per service: a crash or restart loop pulls RunningTaskCount
# below the desired floor. breaching on missing data because a vanished service
# stops publishing the metric, which is itself the failure to catch.
resource "aws_cloudwatch_metric_alarm" "ecs_running_tasks" {
  for_each = toset(var.ecs_service_names)

  alarm_name          = "${local.name}-${each.value}-running-tasks"
  alarm_description   = "${each.value} running tasks dropped below the minimum (crash or restart loop)."
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = var.running_tasks_evaluation_periods
  metric_name         = "RunningTaskCount"
  namespace           = "ECS/ContainerInsights"
  period              = var.running_tasks_period_seconds
  statistic           = "Average"
  threshold           = var.min_running_tasks
  treat_missing_data  = "breaching"

  dimensions = {
    ClusterName = var.cluster_name
    ServiceName = each.value
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# RDS health: sustained high CPU and a low-free-storage floor.
resource "aws_cloudwatch_metric_alarm" "rds_cpu" {
  count = local.rds_enabled ? 1 : 0

  alarm_name          = "${local.name}-rds-cpu"
  alarm_description   = "RDS CPU utilization is sustained high."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.rds_evaluation_periods
  metric_name         = "CPUUtilization"
  namespace           = "AWS/RDS"
  period              = var.rds_period_seconds
  statistic           = "Average"
  threshold           = var.rds_cpu_threshold_percent
  treat_missing_data  = "notBreaching"

  dimensions = {
    DBInstanceIdentifier = var.rds_instance_id
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

resource "aws_cloudwatch_metric_alarm" "rds_free_storage" {
  count = local.rds_enabled ? 1 : 0

  alarm_name          = "${local.name}-rds-free-storage"
  alarm_description   = "RDS free storage dropped below the floor."
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = var.rds_evaluation_periods
  metric_name         = "FreeStorageSpace"
  namespace           = "AWS/RDS"
  period              = var.rds_period_seconds
  statistic           = "Average"
  threshold           = var.rds_free_storage_bytes_threshold
  treat_missing_data  = "notBreaching"

  dimensions = {
    DBInstanceIdentifier = var.rds_instance_id
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# Amazon MQ broker health: sustained high system CPU on the broker node.
resource "aws_cloudwatch_metric_alarm" "mq_cpu" {
  count = local.mq_enabled ? 1 : 0

  alarm_name          = "${local.name}-mq-cpu"
  alarm_description   = "Amazon MQ broker system CPU is sustained high."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.mq_evaluation_periods
  metric_name         = "SystemCpuUtilization"
  namespace           = "AWS/AmazonMQ"
  period              = var.mq_period_seconds
  statistic           = "Average"
  threshold           = var.mq_cpu_threshold_percent
  treat_missing_data  = "notBreaching"

  dimensions = {
    Broker = var.mq_broker_name
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# WAF blocked-request spike (us-east-1, CLOUDFRONT scope). Routes to the
# us-east-1 alerts topic and its own forwarder copy.
resource "aws_cloudwatch_metric_alarm" "waf_blocked" {
  count    = local.waf_enabled ? 1 : 0
  provider = aws.us_east_1

  alarm_name          = "${local.name}-waf-blocked-spike"
  alarm_description   = "WAF blocked-request count spiked above the steady-state threshold."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.waf_blocked_evaluation_periods
  metric_name         = "BlockedRequests"
  namespace           = "AWS/WAFV2"
  period              = var.waf_blocked_period_seconds
  statistic           = "Sum"
  threshold           = var.waf_blocked_threshold
  treat_missing_data  = "notBreaching"

  # A CLOUDFRONT-scoped web ACL publishes BlockedRequests with only the WebACL and
  # Rule dimensions; the Region dimension is required for every protected-resource
  # type EXCEPT CloudFront, so adding it here would leave the alarm permanently in
  # INSUFFICIENT_DATA (CloudWatch matches an alarm to a metric by its exact
  # dimension set). Rule=ALL is the web-ACL-level aggregate.
  dimensions = {
    WebACL = var.waf_web_acl_name
    Rule   = "ALL"
  }

  alarm_actions = [aws_sns_topic.alerts_us_east_1[0].arn]
  ok_actions    = [aws_sns_topic.alerts_us_east_1[0].arn]
}

# ---------------------------------------------------------------------------
# Ingestion queue alarms. These key on the custom metrics the metrics lambda
# publishes (namespace var.queue_metrics_namespace, dimensions Broker/QueueBase),
# so they exist only where the lambda runs (queue_alarms_enabled).
# ---------------------------------------------------------------------------

# A queue holding a backlog while no consumer is attached: workers are down or
# crashed and producers keep filling. Metric math: the backlog counted only while
# consumers < 1. The metrics lambda polls the broker independently of the workers,
# so a stalled queue still emits ConsumerCount=0 / Backlog>0 and fires; a data gap
# means the poller/broker is down (covered by mq_cpu and lambda error metrics), not
# a stalled queue, so missing data must not page here.
resource "aws_cloudwatch_metric_alarm" "queue_backlog_no_consumers" {
  for_each = local.queue_alarms_enabled ? toset(var.queue_bases) : toset([])

  alarm_name          = "${local.name}-queue-${replace(each.value, ".", "-")}-backlog-no-consumers"
  alarm_description   = "Queue ${each.value} holds a backlog while no consumer is attached."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.queue_stall_evaluation_periods
  threshold           = 0
  treat_missing_data  = "notBreaching"

  metric_query {
    id          = "stalled"
    expression  = "IF(consumers < 1, backlog, 0)"
    label       = "BacklogWithoutConsumers"
    return_data = true
  }
  metric_query {
    id = "backlog"
    metric {
      metric_name = "Backlog"
      namespace   = var.queue_metrics_namespace
      period      = var.queue_period_seconds
      stat        = "Maximum"
      dimensions  = { Broker = var.mq_broker_name, QueueBase = each.value }
    }
  }
  metric_query {
    id = "consumers"
    metric {
      metric_name = "ConsumerCount"
      namespace   = var.queue_metrics_namespace
      period      = var.queue_period_seconds
      stat        = "Minimum"
      dimensions  = { Broker = var.mq_broker_name, QueueBase = each.value }
    }
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# Messages parked in a dead-letter queue: something is being dropped and needs an
# operator to inspect and replay it. An absent DLQ (never created, or nothing ever
# dead-lettered) emits no datapoints - the healthy state - so missing data stays
# green.
resource "aws_cloudwatch_metric_alarm" "dlq_depth" {
  for_each = local.queue_alarms_enabled ? toset(var.queue_bases) : toset([])

  alarm_name          = "${local.name}-dlq-${replace(each.value, ".", "-")}-depth"
  alarm_description   = "Messages are parked in the ${each.value} dead-letter queue."
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = var.dlq_evaluation_periods
  metric_name         = "Backlog"
  namespace           = var.queue_metrics_namespace
  period              = var.queue_period_seconds
  statistic           = "Maximum"
  threshold           = 0
  treat_missing_data  = "notBreaching"

  dimensions = {
    Broker    = var.mq_broker_name
    QueueBase = "${each.value}.dlq"
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}

# A source with no successful producer run in the last 24h: a scheduled crawl has
# silently stopped. Keys on the per-source RunSuccess metric each producer emits on
# a finished run. A source that has not run emits no datapoints, so absence IS the
# incident here (unlike the queue alarms) - breaching. A freshly-deployed
# environment fires until the first run completes, which is the intended signal.
resource "aws_cloudwatch_metric_alarm" "no_successful_run" {
  for_each = local.run_alarms_enabled ? toset(var.run_sources) : toset([])

  alarm_name          = "${local.name}-source-${each.value}-no-run-24h"
  alarm_description   = "Source ${each.value} produced no successful ingestion run in the last 24 hours."
  comparison_operator = "LessThanThreshold"
  evaluation_periods  = 1
  metric_name         = "RunSuccess"
  namespace           = var.run_metrics_namespace
  period              = var.no_run_period_seconds
  statistic           = "Sum"
  threshold           = 1
  treat_missing_data  = "breaching"

  dimensions = {
    Source = each.value
  }

  alarm_actions = [aws_sns_topic.alerts.arn]
  ok_actions    = [aws_sns_topic.alerts.arn]
}
