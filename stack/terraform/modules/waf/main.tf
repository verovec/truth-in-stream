data "aws_caller_identity" "current" {
  provider = aws.us_east_1
}

locals {
  name = "${var.project}-${var.environment}"

  # The rate-based rule sits after the managed groups so a managed block on a
  # genuinely bad request wins over a throttle; pick a priority above any group.
  rate_rule_priority = 1000
}

# CLOUDFRONT-scoped web ACL. The default action is allow: legitimate traffic
# passes untouched, and only the managed groups and the rate-based rule block.
# This keeps the "no measurable impact on legitimate users" property while still
# stopping common web attacks and abusive clients.
resource "aws_wafv2_web_acl" "this" {
  provider = aws.us_east_1

  name        = "${local.name}-cloudfront"
  description = "${local.name} CloudFront web ACL: AWS managed rule groups + per-IP rate throttle."
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  dynamic "rule" {
    for_each = var.managed_rule_groups
    content {
      name     = rule.value.name
      priority = rule.value.priority

      # A managed group is driven by override_action, not action. none{} lets the
      # group's own rule actions stand (block); count{} downgrades every rule in
      # the group to count, so a freshly added group can be observed before it is
      # allowed to block.
      override_action {
        dynamic "none" {
          for_each = rule.value.count_only ? [] : [1]
          content {}
        }
        dynamic "count" {
          for_each = rule.value.count_only ? [1] : []
          content {}
        }
      }

      statement {
        managed_rule_group_statement {
          name        = rule.value.name
          vendor_name = rule.value.vendor_name
        }
      }

      visibility_config {
        cloudwatch_metrics_enabled = true
        metric_name                = "${local.name}-${rule.value.name}"
        sampled_requests_enabled   = true
      }
    }
  }

  rule {
    name     = "rate-limit"
    priority = local.rate_rule_priority

    action {
      block {}
    }

    statement {
      rate_based_statement {
        limit                 = var.rate_limit
        aggregate_key_type    = "IP"
        evaluation_window_sec = var.rate_limit_window_seconds
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.name}-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${local.name}-cloudfront"
    sampled_requests_enabled   = true
  }
}

# WAF decision logs. The log group name MUST start with "aws-waf-logs-" or the
# logging configuration is rejected. CloudFront-scoped logs are delivered in
# us-east-1, so the group and its resource policy live there too.
resource "aws_cloudwatch_log_group" "waf" {
  provider = aws.us_east_1

  name              = "aws-waf-logs-${local.name}-cloudfront"
  retention_in_days = var.log_retention_days
}

# Allows the log-delivery service to write WAF logs into the group. Scoped to
# this account and region so it cannot be used as a confused-deputy sink.
data "aws_iam_policy_document" "waf_log_delivery" {
  provider = aws.us_east_1

  statement {
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.waf.arn}:*"]

    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:logs:us-east-1:${data.aws_caller_identity.current.account_id}:*"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_cloudwatch_log_resource_policy" "waf" {
  provider = aws.us_east_1

  policy_name     = "aws-waf-logs-${local.name}-cloudfront"
  policy_document = data.aws_iam_policy_document.waf_log_delivery.json
}

resource "aws_wafv2_web_acl_logging_configuration" "this" {
  provider = aws.us_east_1

  log_destination_configs = [aws_cloudwatch_log_group.waf.arn]
  resource_arn            = aws_wafv2_web_acl.this.arn

  # Redact the Authorization and Cookie headers so session tokens and
  # credentials never land in the decision logs.
  redacted_fields {
    single_header {
      name = "authorization"
    }
  }
  redacted_fields {
    single_header {
      name = "cookie"
    }
  }

  depends_on = [aws_cloudwatch_log_resource_policy.waf]
}
