locals {
  name = "${var.project}-${var.environment}-${var.name}"

  # An empty schedule_expression yields a task definition only - no EventBridge
  # schedule - so an operator can run it on demand with `aws ecs run-task`. A
  # non-empty expression additionally provisions the schedule and its role.
  scheduled = var.schedule_expression != ""

  # entryPoint/command are emitted only when set so an empty override never
  # masks the image's own entrypoint.
  container_overrides = merge(
    length(var.entry_point) > 0 ? { entryPoint = var.entry_point } : {},
    length(var.command) > 0 ? { command = var.command } : {},
  )
}

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

resource "aws_ecs_task_definition" "main" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = var.task_execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([
    merge(
      {
        name      = var.name
        image     = var.image
        essential = true

        environment = [
          for k, v in var.environment_variables : { name = k, value = v }
        ]

        secrets = [
          for k, arn in var.secrets : { name = k, valueFrom = arn }
        ]

        logConfiguration = {
          logDriver = "awslogs"
          options = {
            "awslogs-group"         = var.log_group_name
            "awslogs-region"        = data.aws_region.current.region
            "awslogs-stream-prefix" = var.name
          }
        }
      },
      local.container_overrides,
    )
  ])
}

# Dedicated group: Scheduler presents the schedule GROUP ARN as aws:SourceArn
# (per-schedule scoping is unsupported), so owning the group is what keeps the
# role's trust boundary per-task instead of shared with every schedule in the
# account's default group.
# The scheduler resources gained a count when the schedule became optional. These
# moved blocks map the old count-less addresses to index 0 so an environment that
# already applied a scheduled task (a non-empty expression keeps count at 1) sees
# no destroy/recreate of its live schedule, role, or group.
moved {
  from = aws_scheduler_schedule_group.main
  to   = aws_scheduler_schedule_group.main[0]
}
moved {
  from = aws_iam_role.scheduler
  to   = aws_iam_role.scheduler[0]
}
moved {
  from = aws_iam_role_policy.scheduler
  to   = aws_iam_role_policy.scheduler[0]
}
moved {
  from = aws_scheduler_schedule.main
  to   = aws_scheduler_schedule.main[0]
}

resource "aws_scheduler_schedule_group" "main" {
  count = local.scheduled ? 1 : 0
  name  = local.name
}

data "aws_iam_policy_document" "scheduler_assume" {
  count = local.scheduled ? 1 : 0

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
      values   = [aws_scheduler_schedule_group.main[0].arn]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  count              = local.scheduled ? 1 : 0
  name               = "${local.name}-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume[0].json
}

data "aws_iam_policy_document" "scheduler" {
  count = local.scheduled ? 1 : 0

  statement {
    effect  = "Allow"
    actions = ["ecs:RunTask"]
    # Any revision of this family, nothing else.
    resources = ["${aws_ecs_task_definition.main.arn_without_revision}:*"]

    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }

  statement {
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = [var.task_execution_role_arn, var.task_role_arn]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "scheduler" {
  count  = local.scheduled ? 1 : 0
  name   = "run-task"
  role   = aws_iam_role.scheduler[0].id
  policy = data.aws_iam_policy_document.scheduler[0].json
}

resource "aws_scheduler_schedule" "main" {
  count      = local.scheduled ? 1 : 0
  name       = local.name
  group_name = aws_scheduler_schedule_group.main[0].name

  # The schedule only references the role ARN, so without this an apply that
  # fails between role and policy creation leaves an enabled schedule whose
  # role cannot RunTask.
  depends_on = [aws_iam_role_policy.scheduler]

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression = var.schedule_expression

  target {
    arn      = var.cluster_arn
    role_arn = aws_iam_role.scheduler[0].arn

    ecs_parameters {
      task_definition_arn = aws_ecs_task_definition.main.arn
      launch_type         = "FARGATE"
      task_count          = 1

      network_configuration {
        subnets          = var.subnet_ids
        security_groups  = var.security_group_ids
        assign_public_ip = false
      }
    }

    # Bound retries: the default (185 over 24h) would hammer RunTask on a
    # persistent failure. A missed weekly run is caught by the next one.
    retry_policy {
      maximum_event_age_in_seconds = 3600
      maximum_retry_attempts       = 3
    }
  }
}
