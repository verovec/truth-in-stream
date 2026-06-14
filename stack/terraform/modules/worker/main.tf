locals {
  name = "${var.project}-${var.environment}-${var.name}"

  # entryPoint/command are emitted only when set so an empty override never
  # masks the image's own entrypoint.
  container_overrides = merge(
    length(var.entry_point) > 0 ? { entryPoint = var.entry_point } : {},
    length(var.command) > 0 ? { command = var.command } : {},
  )
}

data "aws_region" "current" {}

# A headless consumer: no portMappings and no load balancer, since the worker
# only makes outbound connections (broker, RDS, Voyage). It scales by replica
# count through desired_count.
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

        # On task stop the worker has stop_timeout seconds to drain in-flight
        # embeds before SIGKILL; whatever it cannot finish stays unacked and the
        # broker redelivers it, so a scale-down or rolling deploy loses no work.
        stopTimeout = var.stop_timeout

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

# EXTERNAL deployment controller: the worker-lifecycle lambda owns rollout (task
# sets) and scale (desired count), so terraform provisions only the service shell.
# task_definition, network_configuration and the rolling-deploy percentages move
# off the service - a task set carries the task definition and network, and the
# lambda creates and promotes it. The aws_ecs_task_definition above stays as the
# base family the lambda registers new image revisions from; the service does not
# reference it directly. On the very first deploy the service has no task set
# until the lambda bootstraps one. See stack/terraform/README.md for the bootstrap
# order.
resource "aws_ecs_service" "main" {
  name          = var.name
  cluster       = var.cluster_id
  desired_count = var.desired_count

  deployment_controller {
    type = "EXTERNAL"
  }

  # Allow `aws ecs execute-command` for operational debugging, matching the other
  # services. The graceful-drain window is the task definition's stopTimeout.
  enable_execute_command = true

  # The lambda owns desired count (scale) and the active task definition
  # (rollout); terraform must not fight it on either after the first apply.
  lifecycle {
    ignore_changes = [desired_count, task_definition]
  }
}
