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
  default     = "workerlifecycle"
  description = "Lambda name, appended to project and environment in resource names."
}

variable "source_binary_path" {
  type        = string
  description = "Path to the prebuilt provided.al2023 `bootstrap` binary (GOOS=linux GOARCH=arm64). Built by `make lambda-workerlifecycle` in stack/backend before apply; the module zips it once and backs all three handler functions with it."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnets the lambda's network interfaces attach to (it reaches the in-VPC broker and the AWS control plane via NAT)."
}

variable "security_group_ids" {
  type        = list(string)
  description = "Security groups attached to the lambda functions. The same group must be in the broker's management_allowed_security_group_ids so the scale and cleanup handlers can read queue depth over the management API."
}

variable "ecs_cluster_name" {
  type        = string
  description = "ECS cluster name the worker services run in. Used in resource-scoped IAM ARNs and passed to the lambda as ECS_CLUSTER."
}

variable "ecs_cluster_arn" {
  type        = string
  description = "ECS cluster ARN, granted on the lambda's ECS actions."
}

variable "task_role_arn" {
  type        = string
  description = "Worker task role ARN the lambda may pass when registering a new task definition revision."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Worker task execution role ARN the lambda may pass when registering a new task definition revision."
}

variable "rabbitmq_url_secret_arn" {
  type        = string
  description = "Secrets Manager ARN holding the broker connection URL. The scale and cleanup handlers read it to reach the management API for queue depth."
}

variable "management_port" {
  type        = number
  default     = 443
  description = "Port the Amazon MQ for RabbitMQ management API is served on (443 over HTTPS for a private broker)."
}

variable "resource_prefix" {
  type        = string
  description = "Prefix (<project>-<environment>) the deploy handler prepends to a service name to derive its task-definition family, matching the worker module's naming."
}

variable "task_subnet_ids" {
  type        = list(string)
  description = "Private subnets a bootstrapped worker task set runs in (the first deploy, when no PRIMARY exists to copy the network from). The worker tasks' subnets, not the lambda's."
}

variable "task_security_group_ids" {
  type        = list(string)
  description = "Security groups a bootstrapped worker task set runs with. The worker tasks' security groups, not the lambda's."
}

variable "scaling_config" {
  type = map(object({
    queue_base       = string
    ratio            = number
    min              = number
    max              = number
    cooldown_seconds = number
  }))
  default     = {}
  description = "Per-service queue-depth scaling policy, keyed by ECS service name. Stored in Parameter Store and read by the scale and cleanup handlers at cold start (the full map can exceed the lambda env-var limit). max = 0 disables a service - the gate that keeps the fleet at zero until the workers move onto ECS. An empty map scales nothing."
}

variable "max_age_hours" {
  type        = number
  default     = 24
  description = "How long the PRIMARY task set must serve before a drained different-version task set is retired, so traffic settles on the new version before the old is torn down."
}

variable "same_version_min_age_minutes" {
  type        = number
  default     = 2
  description = "Minimum age before a superseded same-version-as-PRIMARY task set is deleted."
}

variable "zombie_min_age_minutes" {
  type        = number
  default     = 15
  description = "Minimum age before a task set with zero running tasks is deleted regardless of version or drain state."
}

variable "scale_schedule_expression" {
  type        = string
  default     = "rate(1 minute)"
  description = "EventBridge Scheduler expression for how often the scale handler runs."
}

variable "cleanup_schedule_expression" {
  type        = string
  default     = "rate(1 minute)"
  description = "EventBridge Scheduler expression for how often the cleanup handler runs."
}

variable "memory_size" {
  type        = number
  default     = 256
  description = "Lambda memory in MiB. The handlers fetch a queue page and make a handful of ECS calls."
}

variable "timeout_seconds" {
  type        = number
  default     = 60
  description = "Lambda timeout in seconds. Bounds the slowest ECS rollout path."
}

variable "log_retention_days" {
  type        = number
  default     = 14
  description = "Retention for the lambda functions' CloudWatch log groups."
}
