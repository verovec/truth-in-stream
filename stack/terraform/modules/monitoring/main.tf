data "aws_region" "current" {}

locals {
  name   = "${var.project}-${var.environment}-ingestion"
  region = data.aws_region.current.region

  # A namespace containing a slash must be double-quoted inside a SEARCH metric
  # schema. SEARCH auto-discovers every queue publishing the metric, so a new
  # versioned queue appears in the widget with no dashboard edit.
  ns_quoted = "\"${var.metrics_namespace}\""

  queue_widgets = [
    {
      type   = "metric"
      x      = 0
      y      = 0
      width  = 12
      height = 6
      properties = {
        title  = "Queue backlog by version"
        view   = "timeSeries"
        region = local.region
        period = 60
        yAxis  = { left = { min = 0 } }
        metrics = [
          [{ expression = "SEARCH('{${local.ns_quoted},Broker,Queue} MetricName=\"Backlog\"', 'Average', 60)", label = "", id = "backlog" }]
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
        title  = "Publish rate by version (msg/s)"
        view   = "timeSeries"
        region = local.region
        period = 60
        yAxis  = { left = { min = 0 } }
        metrics = [
          [{ expression = "SEARCH('{${local.ns_quoted},Broker,Queue} MetricName=\"PublishRate\"', 'Average', 60)", label = "", id = "rate" }]
        ]
      }
    },
    {
      type   = "metric"
      x      = 0
      y      = 6
      width  = 12
      height = 6
      properties = {
        title  = "Consumers by version"
        view   = "timeSeries"
        region = local.region
        period = 60
        yAxis  = { left = { min = 0 } }
        metrics = [
          [{ expression = "SEARCH('{${local.ns_quoted},Broker,Queue} MetricName=\"ConsumerCount\"', 'Average', 60)", label = "", id = "consumers" }]
        ]
      }
    },
    {
      type   = "metric"
      x      = 12
      y      = 6
      width  = 12
      height = 6
      properties = {
        title  = "Backlog rollup (${var.queue_base}, all versions)"
        view   = "timeSeries"
        region = local.region
        period = 60
        stat   = "Average"
        yAxis  = { left = { min = 0 } }
        metrics = [
          [var.metrics_namespace, "Backlog", "Broker", var.broker_name, "QueueBase", var.queue_base]
        ]
      }
    },
  ]

  # The worker widgets reference the embedding-worker ECS service; omit them when
  # the worker is not provisioned so the dashboard has no empty panels.
  worker_widgets = var.worker_service_name == "" ? [] : [
    {
      type   = "metric"
      x      = 0
      y      = 12
      width  = 12
      height = 6
      properties = {
        title  = "Worker running tasks"
        view   = "timeSeries"
        region = local.region
        period = 60
        stat   = "Average"
        yAxis  = { left = { min = 0 } }
        metrics = [
          ["ECS/ContainerInsights", "RunningTaskCount", "ClusterName", var.cluster_name, "ServiceName", var.worker_service_name]
        ]
      }
    },
    {
      type   = "metric"
      x      = 12
      y      = 12
      width  = 12
      height = 6
      properties = {
        title  = "Worker CPU / memory utilization (%)"
        view   = "timeSeries"
        region = local.region
        period = 60
        stat   = "Average"
        yAxis  = { left = { min = 0, max = 100 } }
        metrics = [
          ["AWS/ECS", "CPUUtilization", "ClusterName", var.cluster_name, "ServiceName", var.worker_service_name],
          ["AWS/ECS", "MemoryUtilization", "ClusterName", var.cluster_name, "ServiceName", var.worker_service_name]
        ]
      }
    },
  ]
}

resource "aws_cloudwatch_dashboard" "main" {
  dashboard_name = local.name
  dashboard_body = jsonencode({
    widgets = concat(local.queue_widgets, local.worker_widgets)
  })
}
