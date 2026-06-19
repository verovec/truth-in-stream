locals {
  # One time-series line per service for the running-task and unhealthy-target
  # widgets, built from the service/target-group inputs so the dashboard tracks
  # whatever set of services the env actually runs.
  running_task_metrics = [
    for svc in var.ecs_service_names :
    ["ECS/ContainerInsights", "RunningTaskCount", "ClusterName", var.cluster_name, "ServiceName", svc]
  ]

  unhealthy_host_metrics = [
    for svc, suffix in var.target_group_arn_suffixes :
    ["AWS/ApplicationELB", "UnHealthyHostCount", "LoadBalancer", var.alb_arn_suffix, "TargetGroup", suffix]
  ]

  rds_widgets = local.rds_enabled ? [
    {
      type   = "metric"
      x      = 0
      y      = 12
      width  = 12
      height = 6
      properties = {
        title  = "RDS CPU / free storage"
        view   = "timeSeries"
        region = local.region
        period = 60
        stat   = "Average"
        metrics = [
          ["AWS/RDS", "CPUUtilization", "DBInstanceIdentifier", var.rds_instance_id],
          ["AWS/RDS", "FreeStorageSpace", "DBInstanceIdentifier", var.rds_instance_id, { yAxis = "right" }],
        ]
      }
    },
  ] : []

  mq_widgets = local.mq_enabled ? [
    {
      type   = "metric"
      x      = 12
      y      = 12
      width  = 12
      height = 6
      properties = {
        title   = "Amazon MQ broker CPU"
        view    = "timeSeries"
        region  = local.region
        period  = 60
        stat    = "Average"
        yAxis   = { left = { min = 0, max = 100 } }
        metrics = [["AWS/AmazonMQ", "SystemCpuUtilization", "Broker", var.mq_broker_name]]
      }
    },
  ] : []

  # WAF metrics live in us-east-1 (CLOUDFRONT scope), so the widget pins that
  # region explicitly even though the dashboard itself is regional.
  waf_widgets = local.waf_enabled ? [
    {
      type   = "metric"
      x      = 0
      y      = 18
      width  = 12
      height = 6
      properties = {
        title  = "WAF allowed vs blocked"
        view   = "timeSeries"
        region = "us-east-1"
        period = 60
        stat   = "Sum"
        metrics = [
          ["AWS/WAFV2", "BlockedRequests", "WebACL", var.waf_web_acl_name, "Rule", "ALL"],
          ["AWS/WAFV2", "AllowedRequests", "WebACL", var.waf_web_acl_name, "Rule", "ALL"],
        ]
      }
    },
  ] : []

  base_widgets = [
    {
      type   = "metric"
      x      = 0
      y      = 0
      width  = 12
      height = 6
      properties = {
        title  = "ALB requests / 5xx"
        view   = "timeSeries"
        region = local.region
        period = 60
        stat   = "Sum"
        metrics = [
          ["AWS/ApplicationELB", "RequestCount", "LoadBalancer", var.alb_arn_suffix],
          ["AWS/ApplicationELB", "HTTPCode_ELB_5XX_Count", "LoadBalancer", var.alb_arn_suffix, { yAxis = "right" }],
        ]
      }
    },
    {
      type   = "metric"
      x      = 12
      y      = 0
      width  = 12
      height = 6
      properties = {
        title   = "Unhealthy targets by service"
        view    = "timeSeries"
        region  = local.region
        period  = 60
        stat    = "Maximum"
        yAxis   = { left = { min = 0 } }
        metrics = local.unhealthy_host_metrics
      }
    },
    {
      type   = "metric"
      x      = 0
      y      = 6
      width  = 24
      height = 6
      properties = {
        title   = "Running tasks by service"
        view    = "timeSeries"
        region  = local.region
        period  = 60
        stat    = "Average"
        yAxis   = { left = { min = 0 } }
        metrics = local.running_task_metrics
      }
    },
  ]
}

resource "aws_cloudwatch_dashboard" "health" {
  count          = var.create_dashboard ? 1 : 0
  dashboard_name = "${local.name}-health"
  dashboard_body = jsonencode({
    widgets = concat(local.base_widgets, local.rds_widgets, local.mq_widgets, local.waf_widgets)
  })
}
