variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "name" {
  type        = string
  default     = "mqmetrics"
  description = "Lambda name, appended to project and environment in resource names."
}

variable "source_binary_path" {
  type        = string
  description = "Path to the prebuilt provided.al2023 `bootstrap` binary (GOOS=linux GOARCH=arm64). Built by `make lambda-mqmetrics` in stack/backend before apply; the module zips it for deployment."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnets the lambda's network interfaces attach to (it reaches the in-VPC broker and AWS APIs via NAT)."
}

variable "security_group_ids" {
  type        = list(string)
  description = "Security groups attached to the lambda. The same group must be in the broker's management_allowed_security_group_ids so the poller can reach the management API."
}

variable "rabbitmq_url_secret_arn" {
  type        = string
  description = "Secrets Manager ARN holding the broker connection URL. The lambda reads it at runtime to derive the management host and basic-auth credentials."
}

variable "broker_name" {
  type        = string
  description = "Value of the Broker metric dimension on every published datum. Must match the value the dashboard references."
}

variable "queue_names" {
  type        = list(string)
  default     = ["embedding.jobs"]
  description = "Versioned-queue base names. For each, the lambda measures both the base's queues (<base>.v<version>) and its dead-letter queues (<base>.dlq.v<version>), rolling each up under a stable QueueBase name."

  validation {
    condition     = length(var.queue_names) > 0
    error_message = "queue_names must list at least one base queue."
  }
}

variable "metrics_namespace" {
  type        = string
  default     = "TruthInStream/RabbitMQ"
  description = "Custom CloudWatch namespace the lambda publishes to. The metric-publish IAM permission is scoped to this namespace."
}

variable "management_port" {
  type        = number
  default     = 443
  description = "Port the Amazon MQ for RabbitMQ management API is served on (443 over HTTPS for a private broker)."
}

variable "schedule_expression" {
  type        = string
  default     = "rate(1 minute)"
  description = "EventBridge Scheduler expression controlling how often the lambda polls."
}

variable "memory_size" {
  type        = number
  default     = 128
  description = "Lambda memory in MiB. The poller is light: it fetches one JSON page and publishes a small metric batch."
}

variable "timeout_seconds" {
  type        = number
  default     = 30
  description = "Lambda timeout in seconds. Bounds a slow management-API call."
}

variable "log_retention_days" {
  type        = number
  default     = 14
  description = "Retention for the lambda's CloudWatch log group."
}
