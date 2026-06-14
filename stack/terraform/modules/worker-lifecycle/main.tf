locals {
  base = "${var.project}-${var.environment}-${var.name}"

  # Config common to every handler. Per-handler env layers on top of this below.
  common_env = {
    ECS_CLUSTER = var.ecs_cluster_name
  }

  # The three handlers, one zipped binary each selected by LIFECYCLE_HANDLER.
  # scale and cleanup run on a schedule and read queue depth; deploy is invoked by
  # the deploy workflow (no schedule) and needs no broker access. A non-empty
  # schedule marks a function as scheduled.
  functions = {
    scale = {
      schedule = var.scale_schedule_expression
      env = merge(local.common_env, {
        LIFECYCLE_HANDLER       = "scale"
        SCALING_CONFIG_PARAM    = aws_ssm_parameter.scaling_config.name
        RABBITMQ_URL_SECRET_ARN = var.rabbitmq_url_secret_arn
        QUEUE_MANAGEMENT_PORT   = tostring(var.management_port)
      })
    }
    cleanup = {
      schedule = var.cleanup_schedule_expression
      env = merge(local.common_env, {
        LIFECYCLE_HANDLER            = "cleanup"
        SCALING_CONFIG_PARAM         = aws_ssm_parameter.scaling_config.name
        RABBITMQ_URL_SECRET_ARN      = var.rabbitmq_url_secret_arn
        QUEUE_MANAGEMENT_PORT        = tostring(var.management_port)
        MAX_AGE_HOURS                = tostring(var.max_age_hours)
        SAME_VERSION_MIN_AGE_MINUTES = tostring(var.same_version_min_age_minutes)
        ZOMBIE_MIN_AGE_MINUTES       = tostring(var.zombie_min_age_minutes)
      })
    }
    deploy = {
      schedule = ""
      env = merge(local.common_env, {
        LIFECYCLE_HANDLER       = "deploy"
        RESOURCE_PREFIX         = var.resource_prefix
        TASK_SUBNET_IDS         = join(",", var.task_subnet_ids)
        TASK_SECURITY_GROUP_IDS = join(",", var.task_security_group_ids)
      })
    }
  }

  scheduled_functions = { for k, v in local.functions : k => v if v.schedule != "" }
}

data "aws_caller_identity" "current" {}
data "aws_region" "current" {}

# Per-service queue-depth scaling policy. Lives in Parameter Store rather than a
# lambda env var because the full per-service map can exceed the 4 KiB env limit;
# the scale and cleanup handlers read it at cold start. An empty map encodes as
# "{}", which the handlers treat as "scale nothing".
resource "aws_ssm_parameter" "scaling_config" {
  name        = "/${var.project}/${var.environment}/worker-lifecycle/scaling-config"
  description = "Per-service queue-depth scaling policy for the embedding-worker fleet."
  type        = "String"
  value       = jsonencode(var.scaling_config)
}

# --- Lambda execution role -------------------------------------------------
# The managed VPC-access policy grants the ENI lifecycle the lambda needs to
# attach to private subnets plus CloudWatch Logs writes; the inline policy adds
# the scoped ECS, pass-role, secret and parameter permissions.
data "aws_iam_policy_document" "assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = local.base
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "vpc_access" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

data "aws_iam_policy_document" "lambda" {
  # Scale and rollout: read service state and tags, set desired count, and manage
  # task sets. Scoped to the one cluster's services, tasks and task sets.
  statement {
    sid    = "EcsScaleAndRollout"
    effect = "Allow"
    actions = [
      "ecs:DescribeServices",
      "ecs:UpdateService",
      "ecs:TagResource",
      "ecs:CreateTaskSet",
      "ecs:DescribeTaskSets",
      "ecs:UpdateServicePrimaryTaskSet",
      "ecs:DeleteTaskSet",
    ]
    resources = [
      var.ecs_cluster_arn,
      "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:service/${var.ecs_cluster_name}/*",
      "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task/${var.ecs_cluster_name}/*",
      "arn:aws:ecs:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:task-set/${var.ecs_cluster_name}/*/*",
    ]
  }

  # Register and describe task definitions validate against the family, which
  # carries no cluster condition key, so these are not resource-scopable.
  statement {
    sid    = "EcsTaskDefinition"
    effect = "Allow"
    actions = [
      "ecs:DescribeTaskDefinition",
      "ecs:RegisterTaskDefinition",
    ]
    resources = ["*"]
  }

  # Registering a worker task definition revision passes the worker's task roles.
  statement {
    sid       = "PassWorkerTaskRoles"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = [var.task_role_arn, var.task_execution_role_arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }

  statement {
    sid       = "ReadBrokerSecret"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.rabbitmq_url_secret_arn]
  }

  statement {
    sid       = "ReadScalingConfig"
    effect    = "Allow"
    actions   = ["ssm:GetParameter"]
    resources = [aws_ssm_parameter.scaling_config.arn]
  }
}

resource "aws_iam_role_policy" "lambda" {
  name   = "scale-and-rollout"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda.json
}

# --- Lambda functions ------------------------------------------------------
# One zipped bootstrap binary backs all three handlers; LIFECYCLE_HANDLER selects
# which runs. provided.al2023 expects a single executable named `bootstrap` at the
# zip root, which source_file preserves.
data "archive_file" "lambda" {
  type        = "zip"
  source_file = var.source_binary_path
  output_path = "${dirname(var.source_binary_path)}/${var.name}.zip"
}

# Own the log groups so retention is managed and a destroy removes them (an
# auto-created group lingers with never-expire retention).
resource "aws_cloudwatch_log_group" "lambda" {
  for_each = local.functions

  name              = "/aws/lambda/${local.base}-${each.key}"
  retention_in_days = var.log_retention_days
}

resource "aws_lambda_function" "fn" {
  for_each = local.functions

  function_name = "${local.base}-${each.key}"
  description   = "Embedding-worker lifecycle (${each.key}) handler."

  filename         = data.archive_file.lambda.output_path
  source_code_hash = data.archive_file.lambda.output_base64sha256

  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"
  role          = aws_iam_role.lambda.arn

  memory_size = var.memory_size
  timeout     = var.timeout_seconds

  environment {
    variables = each.value.env
  }

  vpc_config {
    subnet_ids         = var.subnet_ids
    security_group_ids = var.security_group_ids
  }

  depends_on = [
    aws_iam_role_policy_attachment.vpc_access,
    aws_cloudwatch_log_group.lambda,
  ]
}

# --- EventBridge Scheduler (scale + cleanup) -------------------------------
# A dedicated schedule group scopes the scheduler role's trust to this group's
# ARN (per-schedule scoping is unsupported), matching the metrics-lambda module so
# the role's trust boundary stays per-workload. The deploy handler has no schedule
# - the deploy workflow invokes it directly.
resource "aws_scheduler_schedule_group" "main" {
  name = local.base
}

data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }

    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_scheduler_schedule_group.main.arn]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  name               = "${local.base}-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler" {
  statement {
    effect    = "Allow"
    actions   = ["lambda:InvokeFunction"]
    resources = [for k in keys(local.scheduled_functions) : aws_lambda_function.fn[k].arn]
  }
}

resource "aws_iam_role_policy" "scheduler" {
  name   = "invoke-lambda"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler.json
}

resource "aws_scheduler_schedule" "main" {
  for_each = local.scheduled_functions

  name       = "${local.base}-${each.key}"
  group_name = aws_scheduler_schedule_group.main.name

  # Without this an apply that fails between role and policy creation could leave
  # an enabled schedule whose role cannot invoke the lambda.
  depends_on = [aws_iam_role_policy.scheduler]

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression = each.value.schedule

  target {
    arn      = aws_lambda_function.fn[each.key].arn
    role_arn = aws_iam_role.scheduler.arn

    # A scale or cleanup tick is fire-and-forget: a missed tick is superseded by
    # the next, so a failed invocation is not retried.
    retry_policy {
      maximum_retry_attempts = 0
    }
  }
}
