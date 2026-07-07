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
  default     = "keycloak"
  description = "Service name."
}

variable "image" {
  type        = string
  description = "Full image URI including tag."
}

variable "container_port" {
  type        = number
  default     = 8080
  description = "Port Keycloak serves traffic on (KC_HTTP_PORT)."
}

variable "management_port" {
  type        = number
  default     = 9000
  description = "Keycloak management port serving /health and /metrics."
}

variable "cpu" {
  type        = number
  default     = 512
  description = "Fargate task CPU units."
}

variable "memory" {
  type        = number
  default     = 1024
  description = "Fargate task memory in MiB."
}

variable "desired_count" {
  type        = number
  default     = 1
  description = "Number of running tasks."
}

variable "environment_variables" {
  type        = map(string)
  default     = {}
  description = "Plain environment variables (KC_* config that is not secret)."
}

variable "secrets" {
  type        = map(string)
  default     = {}
  description = "Environment variables injected from Secrets Manager: name -> secret ARN."
}

variable "cluster_id" {
  type        = string
  description = "ECS cluster."
}

variable "subnet_ids" {
  type        = list(string)
  description = "Subnets for the tasks (private)."
}

variable "security_group_id" {
  type        = string
  description = "Shared security group for the tasks."
}

variable "alb_security_group_id" {
  type        = string
  description = "ALB security group allowed to reach this service's ports."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Role ECS uses to pull the image and inject secrets."
}

variable "task_role_arn" {
  type        = string
  description = "Role the container assumes at runtime."
}

variable "log_group_name" {
  type        = string
  description = "CloudWatch log group for container logs."
}

variable "vpc_id" {
  type        = string
  description = "VPC for the target group."
}

variable "listener_arn" {
  type        = string
  description = "ALB listener to attach the host-header routing rule to."
}

variable "listener_rule_priority" {
  type        = number
  description = "Priority of the routing rule (lower wins). Keep below the app path rules."
}

variable "host_headers" {
  type        = list(string)
  description = "Host headers routed to Keycloak (e.g. [\"login.jeminforme.fr\"])."
}

variable "health_check_path" {
  type        = string
  default     = "/health/ready"
  description = "Target group health check path (served on the management port)."
}
