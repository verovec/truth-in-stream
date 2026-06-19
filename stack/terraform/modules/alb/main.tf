locals {
  name  = "${var.project}-${var.environment}"
  https = var.certificate_arn != ""

  # An internal load balancer lives in the private subnets and is fronted by
  # CloudFront via a VPC origin; a public one keeps the legacy internet-facing
  # placement in the public subnets. The internal path owns a restricted SG
  # (origin-facing CloudFront prefix list only); the public path reuses the SG
  # the caller passes in.
  subnets         = var.internal ? var.private_subnet_ids : var.public_subnet_ids
  security_groups = var.internal ? [aws_security_group.internal[0].id] : [var.security_group_id]
}

# Origin-facing CloudFront IP ranges, published by AWS as a managed prefix list.
# Restricting the internal ALB's ingress to this list means only CloudFront's
# VPC origin can reach it - the ALB has no public DNS and no open ingress.
data "aws_ec2_managed_prefix_list" "cloudfront_origin_facing" {
  count = var.internal ? 1 : 0
  name  = "com.amazonaws.global.cloudfront.origin-facing"
}

resource "aws_security_group" "internal" {
  count = var.internal ? 1 : 0

  name        = "${local.name}-alb-internal"
  description = "Internal ALB; ingress only from the CloudFront origin-facing prefix list"
  vpc_id      = var.vpc_id

  lifecycle {
    precondition {
      condition     = var.vpc_id != "" && length(var.private_subnet_ids) > 0
      error_message = "internal = true requires vpc_id and private_subnet_ids to be set."
    }
  }

  ingress {
    description     = "HTTP from CloudFront origin-facing ranges"
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    prefix_list_ids = [data.aws_ec2_managed_prefix_list.cloudfront_origin_facing[0].id]
  }

  dynamic "ingress" {
    for_each = local.https ? [1] : []

    content {
      description     = "HTTPS from CloudFront origin-facing ranges"
      from_port       = 443
      to_port         = 443
      protocol        = "tcp"
      prefix_list_ids = [data.aws_ec2_managed_prefix_list.cloudfront_origin_facing[0].id]
    }
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${local.name}-alb-internal" }
}

resource "aws_lb" "main" {
  name               = local.name
  internal           = var.internal
  load_balancer_type = "application"
  subnets            = local.subnets
  security_groups    = local.security_groups

  enable_deletion_protection = var.deletion_protection
}

# Services attach their own listener rules; anything unmatched is a 404 rather
# than an accidental default backend.
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"

  dynamic "default_action" {
    for_each = local.https ? [] : [1]

    content {
      type = "fixed-response"

      fixed_response {
        content_type = "text/plain"
        message_body = "not found"
        status_code  = "404"
      }
    }
  }

  dynamic "default_action" {
    for_each = local.https ? [1] : []

    content {
      type = "redirect"

      redirect {
        port        = "443"
        protocol    = "HTTPS"
        status_code = "HTTP_301"
      }
    }
  }
}

resource "aws_lb_listener" "https" {
  count = local.https ? 1 : 0

  load_balancer_arn = aws_lb.main.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn

  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "text/plain"
      message_body = "not found"
      status_code  = "404"
    }
  }
}
