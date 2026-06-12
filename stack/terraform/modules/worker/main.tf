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

# On the very first apply the ECR image does not exist yet, so tasks fail to
# pull until the deploy workflow pushes one. The provider does not wait for
# steady state, so apply still converges; the service self-heals on first
# deploy. See stack/terraform/README.md for the bootstrap order.
resource "aws_ecs_service" "main" {
  name            = var.name
  cluster         = var.cluster_id
  task_definition = aws_ecs_task_definition.main.arn
  desired_count   = var.desired_count

  # No launch_type: inherits the cluster default capacity provider strategy
  # (FARGATE base, Spot above it).

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = var.security_group_ids
    assign_public_ip = false
  }

  # Allow `aws ecs execute-command` for operational debugging, matching the other
  # services. The graceful-drain window is the task definition's stopTimeout.
  enable_execute_command = true

  # One replica can stop before a replacement is healthy: the broker requeues
  # whatever an old task had not acked, so a rolling deploy loses no work and
  # need not double the fleet.
  deployment_maximum_percent         = 200
  deployment_minimum_healthy_percent = 50
}
