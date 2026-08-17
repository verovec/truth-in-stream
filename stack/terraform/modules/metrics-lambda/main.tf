locals {
  name = "${var.project}-${var.environment}-${var.name}"
}

data "aws_caller_identity" "current" {}

# Execution role. The managed VPC-access policy grants the ENI lifecycle the
# lambda needs to attach to private subnets plus CloudWatch Logs writes; the
# inline policy adds the scoped secret read and the namespaced metric publish.
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
  name               = local.name
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "vpc_access" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

data "aws_iam_policy_document" "lambda" {
  statement {
    sid       = "ReadBrokerSecret"
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.rabbitmq_url_secret_arn]
  }

  # PutMetricData has no resource-level scoping, so the namespace condition is
  # what keeps the permission least-privilege: the lambda can only write to its
  # own metric namespace, nothing else in CloudWatch.
  statement {
    sid       = "PublishMetrics"
    effect    = "Allow"
    actions   = ["cloudwatch:PutMetricData"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "cloudwatch:namespace"
      values   = [var.metrics_namespace]
    }
  }
}

resource "aws_iam_role_policy" "lambda" {
  name   = "poll-and-publish"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda.json
}

# Own the log group so retention is managed and a destroy removes it (an
# auto-created group lingers with never-expire retention).
resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/aws/lambda/${local.name}"
  retention_in_days = var.log_retention_days
}

# Zip the prebuilt bootstrap binary. provided.al2023 expects a single executable
# named `bootstrap` at the zip root, which source_file preserves.
data "archive_file" "lambda" {
  type        = "zip"
  source_file = var.source_binary_path
  output_path = "${dirname(var.source_binary_path)}/${var.name}.zip"
}

resource "aws_lambda_function" "main" {
  function_name = local.name
  description   = "Polls the RabbitMQ management API and publishes per-queue CloudWatch metrics."

  filename         = data.archive_file.lambda.output_path
  source_code_hash = data.archive_file.lambda.output_base64sha256

  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"
  role          = aws_iam_role.lambda.arn

  memory_size = var.memory_size
  timeout     = var.timeout_seconds

  environment {
    variables = {
      RABBITMQ_URL_SECRET_ARN = var.rabbitmq_url_secret_arn
      METRICS_NAMESPACE       = var.metrics_namespace
      BROKER_NAME             = var.broker_name
      QUEUE_NAMES             = join(",", var.queue_names)
      MANAGEMENT_PORT         = tostring(var.management_port)
    }
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

# EventBridge Scheduler invocation. A dedicated schedule group scopes the
# scheduler role's trust to this schedule's group ARN (per-schedule scoping is
# unsupported), mirroring the scheduled-task module so the role's trust boundary
# stays per-workload instead of the account-wide default group.
resource "aws_scheduler_schedule_group" "main" {
  name = local.name
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
  name               = "${local.name}-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler" {
  statement {
    effect    = "Allow"
    actions   = ["lambda:InvokeFunction"]
    resources = [aws_lambda_function.main.arn]
  }
}

resource "aws_iam_role_policy" "scheduler" {
  name   = "invoke-lambda"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler.json
}

resource "aws_scheduler_schedule" "main" {
  name       = local.name
  group_name = aws_scheduler_schedule_group.main.name

  # Without this an apply that fails between role and policy creation could leave
  # an enabled schedule whose role cannot invoke the lambda.
  depends_on = [aws_iam_role_policy.scheduler]

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression = var.schedule_expression

  target {
    arn      = aws_lambda_function.main.arn
    role_arn = aws_iam_role.scheduler.arn

    # A metrics poll is fire-and-forget: a missed tick is superseded by the next
    # one, so a failed invocation is not retried (a late publish would only write
    # stale points). Lambda's own async-invoke retries are bypassed by Scheduler.
    retry_policy {
      maximum_retry_attempts = 0
    }
  }
}
