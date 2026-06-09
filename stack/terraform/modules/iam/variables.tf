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

variable "github_deploy_ref" {
  type        = string
  default     = "refs/heads/main"
  description = "Git ref whose workflow runs may assume the deploy role. Never widen to a wildcard; PR-time previews get their own weaker role if ever needed."
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
