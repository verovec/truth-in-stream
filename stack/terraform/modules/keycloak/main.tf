locals {
  name = "${var.project}-${var.environment}-${var.name}"
}

data "aws_region" "current" {}

# Least-privilege ingress from the internal ALB: the traffic port (login
# requests) and the management port (health checks). Keycloak serves
# /health/ready on the management port only, so the ALB target group health
# check needs to reach it separately from the traffic port.
resource "aws_vpc_security_group_ingress_rule" "traffic_from_alb" {
  security_group_id            = var.security_group_id
  referenced_security_group_id = var.alb_security_group_id
  from_port                    = var.container_port
  to_port                      = var.container_port
  ip_protocol                  = "tcp"
  description                  = "${var.name} traffic port from ALB"
}

resource "aws_vpc_security_group_ingress_rule" "health_from_alb" {
  security_group_id            = var.security_group_id
  referenced_security_group_id = var.alb_security_group_id
  from_port                    = var.management_port
  to_port                      = var.management_port
  ip_protocol                  = "tcp"
  description                  = "${var.name} management/health port from ALB"
}

resource "aws_ecs_task_definition" "main" {
  family                   = local.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = var.task_execution_role_arn
  task_role_arn            = var.task_role_arn

  container_definitions = jsonencode([
    {
      name      = var.name
      image     = var.image
      essential = true

      portMappings = [
        {
          containerPort = var.container_port
          protocol      = "tcp"
        },
        {
          containerPort = var.management_port
          protocol      = "tcp"
        },
      ]

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
    }
  ])
}

resource "aws_lb_target_group" "main" {
  name        = local.name
  port        = var.container_port
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = var.vpc_id

  deregistration_delay = 30

  # Health check on the management port: Keycloak exposes /health/ready there,
  # not on the traffic port.
  health_check {
    path                = var.health_check_path
    port                = tostring(var.management_port)
    matcher             = "200"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

# Host-based routing: login.<domain> reaches Keycloak. CloudFront forwards the
# viewer Host header (AllViewer origin-request policy), so the ALB can match on
# it. Priority is below the backend/frontend path rules so this host wins over
# the frontend catch-all.
resource "aws_lb_listener_rule" "main" {
  listener_arn = var.listener_arn
  priority     = var.listener_rule_priority

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.main.arn
  }

  condition {
    host_header {
      values = var.host_headers
    }
  }
}

# On the very first apply the ECR image does not exist yet, so tasks fail to
# pull until the deploy workflow pushes one. The provider does not wait for
# steady state, so apply still converges; the service self-heals on first
# deploy.
resource "aws_ecs_service" "main" {
  name            = var.name
  cluster         = var.cluster_id
  task_definition = aws_ecs_task_definition.main.arn
  desired_count   = var.desired_count

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = [var.security_group_id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.main.arn
    container_name   = var.name
    container_port   = var.container_port
  }

  # Keycloak imports the realm and starts Quarkus on boot, so it needs a longer
  # grace period than a plain app before health checks count against it.
  health_check_grace_period_seconds = 180
  enable_execute_command            = true

  deployment_maximum_percent         = 200
  deployment_minimum_healthy_percent = 100

  depends_on = [aws_lb_listener_rule.main]

  # The deploy pipeline owns the active task definition: the tag release pins
  # the service to an immutable sha-<7> revision (_deploy.yml), and an apply
  # must not revert it to this resource's revision (a transient downgrade to
  # :latest). Terraform still registers new revisions; the next pinned roll's
  # describe-task-definition picks up the latest one. Mirrors modules/worker.
  lifecycle {
    ignore_changes = [task_definition]
  }
}
