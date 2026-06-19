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
  description = "Service name (backend, frontend)."
}

variable "image" {
  type        = string
  description = "Full image URI including tag."
}

variable "container_port" {
  type        = number
  description = "Port the container listens on."
}

variable "cpu" {
  type        = number
  default     = 256
  description = "Fargate task CPU units."
}

variable "memory" {
  type        = number
  default     = 512
  description = "Fargate task memory in MiB."
}

variable "desired_count" {
  type        = number
  default     = 1
  description = "Number of running tasks."
}

variable "capacity_provider_strategy" {
  type = list(object({
    capacity_provider = string
    weight            = number
    base              = optional(number, 0)
  }))
  default     = []
  description = "Optional Fargate capacity-provider strategy for the service. Empty (the default) inherits the cluster's default strategy (on-demand FARGATE). Set it to place a service on FARGATE_SPOT (cheaper, interruptible) — suitable only for stateless, interruption-tolerant services; keep long-lived-connection services on FARGATE. capacity_provider_strategy and launch_type are mutually exclusive on aws_ecs_service, so the module sets neither when this is empty and only this when it is set."

  validation {
    condition = alltrue([
      for s in var.capacity_provider_strategy :
      contains(["FARGATE", "FARGATE_SPOT"], s.capacity_provider)
    ])
    error_message = "capacity_provider must be FARGATE or FARGATE_SPOT."
  }
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
  description = "ALB security group allowed to reach this service's container port."
}

variable "task_execution_role_arn" {
  type        = string
  description = "Role ECS uses to pull the image and inject secrets."
}

variable "task_role_arn" {
  type        = string
  description = "Role the application assumes at runtime."
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
  description = "ALB listener to attach the routing rule to."
}

variable "listener_rule_priority" {
  type        = number
  description = "Priority of the routing rule (lower wins)."
}

variable "path_patterns" {
  type        = list(string)
  description = "Path patterns routed to this service (e.g. [\"/api/*\", \"/healthz\"])."
}

variable "health_check_path" {
  type        = string
  description = "Target group health check path."
}
