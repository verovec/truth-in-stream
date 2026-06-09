locals {
  name = "${var.project}-${var.environment}-migrate"
}

data "aws_region" "current" {}

# One-shot Fargate task: the deploy workflow runs it and waits for exit 0
# before rolling the services. The migrate CLI reads the database URL from a
# flag, not the environment, so a shell wrapper expands the injected secret.
resource "aws_ecs_task_definition" "migrate" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = 256
  memory                   = 512
  execution_role_arn       = var.task_execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([
    {
      name       = "migrate"
      image      = var.image
      essential  = true
      entryPoint = ["/bin/sh", "-c"]
      command    = ["migrate -path=/migrations -database \"$DATABASE_URL\" up"]

      secrets = [
        {
          name      = "DATABASE_URL"
          valueFrom = var.dsn_secret_arn
        }
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = var.log_group_name
          "awslogs-region"        = data.aws_region.current.region
          "awslogs-stream-prefix" = "migrate"
        }
      }
    }
  ])
}
