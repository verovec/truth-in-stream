variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "log_retention_days" {
  type        = number
  default     = 30
  description = "CloudWatch log retention for task logs."
}
