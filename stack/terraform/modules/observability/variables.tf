variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "slack_webhook_secret_arn" {
  type        = string
  description = "Secrets Manager ARN holding the Slack incoming-webhook URL. The forwarder Lambda reads it at runtime; the URL is never committed or passed as a plaintext variable."
}

variable "log_retention_days" {
  type        = number
  default     = 30
  description = "Finite CloudWatch retention (days) for the forwarder Lambda's own log group, so monitoring infrastructure does not itself grow logs unbounded. Mirrors the cost-baseline log_retention_days."
}

# --- Targets the alarms are dimensioned against ---

variable "alb_arn_suffix" {
  type        = string
  description = "ARN suffix of the application load balancer (the LoadBalancer dimension on AWS/ApplicationELB metrics)."
}

variable "target_group_arn_suffixes" {
  type        = map(string)
  description = "Per-service target-group ARN suffixes keyed by service name, for the unhealthy-target alarms (the TargetGroup dimension on AWS/ApplicationELB metrics)."
}

variable "cluster_name" {
  type        = string
  description = "ECS cluster name carrying the monitored services."
}

variable "ecs_service_names" {
  type        = list(string)
  description = "ECS service names to watch for running-task drops (backend, frontend, and any enabled workers)."
}

variable "rds_instance_id" {
  type        = string
  default     = ""
  description = "RDS DB instance identifier (the DBInstanceIdentifier dimension). Empty disables the RDS alarms when the env runs no managed database."
}

variable "mq_broker_name" {
  type        = string
  default     = ""
  description = "Amazon MQ broker name, the Broker dimension value on AWS/AmazonMQ metrics (the dimension is the broker name, not its id). Empty disables the broker alarm."
}

variable "waf_web_acl_name" {
  type        = string
  default     = ""
  description = "WAFv2 web ACL name (the WebACL dimension on AWS/WAFV2 metrics). Empty disables the WAF blocked-request alarm."
}

# --- Alarm thresholds (all variable-driven to avoid alert spam) ---

variable "alb_5xx_threshold" {
  type        = number
  default     = 10
  description = "ALB-generated 5xx responses over the evaluation window before the alarm fires. Counts HTTPCode_ELB_5XX_Count (load-balancer faults), not application 5xx."
}

variable "alb_5xx_period_seconds" {
  type        = number
  default     = 300
  description = "Evaluation period (seconds) for the ALB 5xx alarm."
}

variable "alb_5xx_evaluation_periods" {
  type        = number
  default     = 1
  description = "Consecutive breaching periods before the ALB 5xx alarm fires."
}

variable "unhealthy_host_threshold" {
  type        = number
  default     = 1
  description = "Unhealthy targets in a service's target group before the alarm fires (>= this count)."
}

variable "unhealthy_host_period_seconds" {
  type        = number
  default     = 60
  description = "Evaluation period (seconds) for the unhealthy-target alarms."
}

variable "unhealthy_host_evaluation_periods" {
  type        = number
  default     = 3
  description = "Consecutive breaching periods before an unhealthy-target alarm fires, so a single deregistration blip does not page."
}

variable "min_running_tasks" {
  type        = number
  default     = 1
  description = "Minimum running tasks per service; the alarm fires when RunningTaskCount drops below this (a crash/restart loop)."
}

variable "running_tasks_period_seconds" {
  type        = number
  default     = 60
  description = "Evaluation period (seconds) for the ECS running-task alarms."
}

variable "running_tasks_evaluation_periods" {
  type        = number
  default     = 5
  description = "Consecutive breaching periods before an ECS running-task alarm fires, so a normal rollout (brief dip) does not page."
}

variable "rds_cpu_threshold_percent" {
  type        = number
  default     = 85
  description = "RDS CPUUtilization percent that, sustained, raises the database-health alarm."
}

variable "rds_free_storage_bytes_threshold" {
  type        = number
  default     = 2147483648
  description = "RDS FreeStorageSpace floor in bytes; the alarm fires below it. Default 2 GiB."
}

variable "rds_period_seconds" {
  type        = number
  default     = 300
  description = "Evaluation period (seconds) for the RDS alarms."
}

variable "rds_evaluation_periods" {
  type        = number
  default     = 3
  description = "Consecutive breaching periods before an RDS alarm fires."
}

variable "mq_cpu_threshold_percent" {
  type        = number
  default     = 85
  description = "Amazon MQ SystemCpuUtilization percent that, sustained, raises the broker-health alarm."
}

variable "mq_period_seconds" {
  type        = number
  default     = 300
  description = "Evaluation period (seconds) for the Amazon MQ alarm."
}

variable "mq_evaluation_periods" {
  type        = number
  default     = 3
  description = "Consecutive breaching periods before the Amazon MQ alarm fires."
}

variable "waf_blocked_threshold" {
  type        = number
  default     = 1000
  description = "WAF BlockedRequests over the evaluation window before the spike alarm fires. Set above the steady-state block rate so routine bot blocks do not page; a sudden surge (attack) does."
}

variable "waf_blocked_period_seconds" {
  type        = number
  default     = 300
  description = "Evaluation period (seconds) for the WAF blocked-request spike alarm."
}

variable "waf_blocked_evaluation_periods" {
  type        = number
  default     = 1
  description = "Consecutive breaching periods before the WAF blocked-request alarm fires."
}

variable "create_dashboard" {
  type        = bool
  default     = true
  description = "Create a CloudWatch dashboard summarising the key health signals (ALB 5xx, unhealthy targets, ECS running tasks, RDS, MQ, WAF blocks)."
}
