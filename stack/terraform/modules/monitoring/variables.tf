variable "project" {
  type        = string
  description = "Project slug used in the dashboard name."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "metrics_namespace" {
  type        = string
  description = "Custom CloudWatch namespace the metrics lambda publishes queue metrics to."
}

variable "broker_name" {
  type        = string
  description = "Value of the Broker dimension on the published metrics. Must match what the lambda publishes."
}

variable "queue_base" {
  type        = string
  description = "Versioned-queue base name the rollup metrics are published under (the QueueBase dimension value), e.g. embedding.jobs."
}

variable "cluster_name" {
  type        = string
  description = "ECS cluster name carrying the embedding-worker service, for the AWS/ECS and Container Insights worker widgets."
}

variable "worker_service_name" {
  type        = string
  default     = ""
  description = "Embedding-worker ECS service name. Empty when the worker is not provisioned, in which case the worker widgets are omitted."
}
