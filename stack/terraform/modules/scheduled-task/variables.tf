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
  description = "Task name, appended to project and environment in resource names."
}

variable "image" {
  type        = string
  description = "Container image URI the task runs."
}

variable "entry_point" {
  type        = list(string)
  description = "Container entryPoint override. Required so a forgotten value fails at plan time instead of silently running the image's default binary; pass [] to keep the image's own entrypoint."
}

variable "command" {
  type        = list(string)
  description = "Container command (arguments to the entrypoint). Pass [] to keep the image default."
}

variable "schedule_expression" {
  type        = string
  description = "EventBridge Scheduler expression, e.g. cron(0 3 ? * SUN *) or rate(7 days)."
}

variable "cpu" {
  type        = number
  description = "Fargate task CPU units (1024 = 1 vCPU)."
}

variable "memory" {
  type        = number
  description = "Fargate task memory in MiB. Must be a valid combination with cpu."
}

variable "environment_variables" {
  type        = map(string)
  default     = {}
  description = "Plain environment variables for the container."
}

variable "secrets" {
  type        = map(string)
  default     = {}
  description = "Environment variables injected from Secrets Manager, name to secret ARN."
}

variable "cluster_arn" {
  type        = string
  description = "ECS cluster ARN the scheduled task runs in."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Subnets for the task's network interface (private; no public IP is assigned)."
}

variable "security_group_ids" {
  type        = list(string)
  description = "Security groups attached to the task."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Role ECS uses to pull the image and inject secrets."
}

variable "task_role_arn" {
  type        = string
  description = "Role the task assumes at runtime."
}

variable "log_group_name" {
  type        = string
  description = "CloudWatch log group for task logs."
}
