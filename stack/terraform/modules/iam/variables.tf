variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "github_repository" {
  type        = string
  description = "GitHub org/repo allowed to assume the deploy role (e.g. verovec/truth-in-stream)."
}

variable "github_deploy_refs" {
  type        = list(string)
  default     = ["refs/heads/main"]
  description = "Git refs whose workflow runs may assume the deploy role, matched with StringLike on the OIDC sub. Each entry may use a bounded glob (e.g. refs/tags/v* for the tag-release path). Never set a blanket wildcard (refs/*) — PR branches and forks must never assume the deploy role; PR-time previews get their own weaker role if ever needed."
}

variable "create_oidc_provider" {
  type        = bool
  default     = true
  description = "Create the GitHub OIDC provider. Set false if the account already has one (it is account-global)."
}

variable "ecr_repository_arns" {
  type        = list(string)
  description = "ECR repositories the deploy role may push to."
}

variable "secret_arns" {
  type        = list(string)
  description = "Secrets Manager ARNs the task execution role may read (injected as container secrets)."
}

variable "cluster_arn" {
  type        = string
  description = "ECS cluster the deploy role operates on."
}

variable "ssm_parameter_arns" {
  type        = list(string)
  default     = []
  description = "SSM parameters the deploy role may read (network config for run-task)."
}

variable "media_bucket_arn" {
  type        = string
  default     = ""
  description = "S3 media bucket ARN the application task role may read and write. Empty disables the grant."
}

variable "db_backup_bucket_arn" {
  type        = string
  default     = ""
  description = "S3 database-backup bucket ARN the task role may write dumps to (s3:PutObject). Empty disables the grant."
}
