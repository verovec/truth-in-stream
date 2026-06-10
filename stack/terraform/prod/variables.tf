variable "aws_region" {
  type        = string
  default     = "eu-west-3"
  description = "AWS region for all resources."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."

  validation {
    condition     = contains(["dev", "prod"], var.environment)
    error_message = "environment must be dev or prod."
  }
}

variable "media_cors_allowed_origins" {
  type        = list(string)
  default     = ["*"]
  description = "Browser origins allowed to PUT/GET media objects directly via presigned URLs. Defaults to any origin while there is no fixed frontend domain; restrict to the app origin once one exists."
}

variable "enable_wiki_sync" {
  type        = bool
  default     = false
  description = "Create the scheduled Wikipedia delta-sync task. Keep false until the wikisync binary ships in the backend image."
}

variable "wiki_corpus" {
  type        = string
  default     = "simplewiki"
  description = "Wikipedia corpus the sync targets (e.g. simplewiki, enwiki). See modules/scheduled-task/README.md for RDS sizing before enwiki."
}

variable "wiki_sync_schedule" {
  type        = string
  default     = "cron(0 3 ? * SUN *)"
  description = "EventBridge Scheduler expression for the weekly delta sync (UTC)."
}

variable "wiki_sync_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the wikisync task (1024 = 1 vCPU)."
}

variable "wiki_sync_memory" {
  type        = number
  default     = 4096
  description = "Fargate memory in MiB for the wikisync task."
}
