variable "project" {
  type        = string
  description = "Project slug used to namespace resources."
}

variable "environment" {
  type        = string
  description = "Deployment environment (e.g. prod)."
}

variable "name" {
  type        = string
  description = "Service name (e.g. embedworker)."
}

variable "image" {
  type        = string
  description = "Container image URI."
}

variable "entry_point" {
  type        = list(string)
  default     = []
  description = "Container entryPoint override; empty keeps the image's own."
}

variable "command" {
  type        = list(string)
  default     = []
  description = "Container command override; empty keeps the image's own."
}

variable "cpu" {
  type        = number
  description = "Task CPU units (1024 = 1 vCPU)."
}

variable "memory" {
  type        = number
  description = "Task memory in MiB."
}

variable "desired_count" {
  type        = number
  description = "Number of worker replicas to run. Scale this to scale embedding throughput."
}

variable "stop_timeout" {
  type        = number
  default     = 120
  description = "Seconds between SIGTERM and SIGKILL on task stop, giving an in-flight embed room to finish or requeue before the container is force-killed. Fargate caps this at 120."
}

variable "environment_variables" {
  type        = map(string)
  default     = {}
  description = "Plain environment variables."
}

variable "secrets" {
  type        = map(string)
  default     = {}
  description = "Environment variables injected from Secrets Manager: name -> secret ARN."
}

variable "cluster_id" {
  type        = string
  description = "ECS cluster ID the service runs in."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Private subnets for the tasks."
}

variable "security_group_ids" {
  type        = list(string)
  description = "Security groups for the tasks. The worker is outbound-only (broker, RDS, Voyage); no ingress rule is added."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Task execution role ARN (pulls the image, reads secrets, writes logs)."
}

variable "task_role_arn" {
  type        = string
  description = "Task role ARN (the worker's own AWS permissions)."
}

variable "log_group_name" {
  type        = string
  description = "CloudWatch log group for the worker's logs."
}
