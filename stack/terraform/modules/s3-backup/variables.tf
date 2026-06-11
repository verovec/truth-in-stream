variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "retention_days" {
  type        = number
  default     = 30
  description = "Days after which a current backup object expires."

  validation {
    condition     = var.retention_days > 0
    error_message = "retention_days must be greater than 0."
  }
}

variable "noncurrent_retention_days" {
  type        = number
  default     = 7
  description = "Days a noncurrent (overwritten or deleted) backup version is retained before expiry."

  validation {
    condition     = var.noncurrent_retention_days > 0
    error_message = "noncurrent_retention_days must be greater than 0."
  }
}
