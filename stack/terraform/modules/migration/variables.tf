variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "image" {
  type        = string
  description = "Migration image URI (migrate CLI + the repo's migration files)."
}

variable "dsn_secret_arn" {
  type        = string
  description = "Secrets Manager ARN holding the DATABASE_URL."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Role ECS uses to pull the image and inject the DSN."
}

variable "task_role_arn" {
  type        = string
  description = "Role the task assumes at runtime."
}

variable "log_group_name" {
  type        = string
  description = "CloudWatch log group for migration logs."
}
