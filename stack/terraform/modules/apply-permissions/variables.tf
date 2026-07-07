variable "include_acm" {
  type        = bool
  default     = false
  description = "Include the ACM certificate actions (modules/acm). Set true in the env that requests the public TLS certificate (prod); the DNS validation records live in the main account, so no Route53 action is added here."
}

variable "include_cloudfront" {
  type        = bool
  default     = false
  description = "Include the CloudFront distribution + VPC origin actions (modules/cloudfront). Set true in the env that fronts the internal ALB with CloudFront (prod)."
}

variable "include_waf" {
  type        = bool
  default     = false
  description = "Include the CLOUDFRONT-scoped WAFv2 web ACL actions, its logging configuration, and the CloudWatch log resource-policy actions (modules/waf). Set true in the env that fronts CloudFront with a web ACL (prod)."
}

variable "include_rds" {
  type        = bool
  default     = false
  description = "Include the RDS management actions. Set from the env's enable_rds so the manifest only demands RDS permissions when the plan actually provisions RDS (no false positives when RDS is off)."
}

variable "include_scheduled_tasks" {
  type        = bool
  default     = false
  description = "Include EventBridge Scheduler + scheduled-task actions. Set true when the env creates any scheduled Fargate task (wikisync or db-backup)."
}

variable "include_bastion" {
  type        = bool
  default     = false
  description = "Include the SSM bastion actions (EC2 instance lifecycle + instance profile). Set from the env's enable_bastion so the manifest only demands them when the plan provisions the bastion."
}

variable "include_ingestion_hosts" {
  type        = bool
  default     = false
  description = "Include the ingestion-host actions (the crawler + consumer EC2 instances and their instance profiles). Set from the env's enable_ingestion_hosts so the manifest only demands them when the plan provisions the hosts. Same EC2 instance + instance-profile lifecycle as the bastion; the hosts' runtime Secrets Manager / ECR-pull / CloudWatch Logs permissions live on each host's own instance role (created via iam:PutRolePolicy, covered by iam_actions), not on the apply role."
}

variable "include_metrics_lambda" {
  type        = bool
  default     = false
  description = "Include the metrics-poller lambda, its CloudWatch dashboard, and EventBridge Scheduler actions. Set from the env's enable_metrics_lambda so the manifest only demands them when the plan provisions the lambda."
}

variable "include_worker_lifecycle" {
  type        = bool
  default     = false
  description = "Include the worker-lifecycle lambda functions and their EventBridge Scheduler actions. Set from the env's enable_worker_lifecycle so the manifest only demands them when the plan provisions the lambda. The lambda's runtime ECS/scaling permissions live on its own execution role (covered by iam_actions), not on the apply role; its scaling-config parameter is covered by ssm_actions."
}

variable "include_observability" {
  type        = bool
  default     = false
  description = "Include the observability module's actions: CloudWatch alarms, the alerts SNS topic, the Slack forwarder lambda (function lifecycle + resource policy), and the health dashboard. Set true in the env that provisions monitoring + alerting (prod)."
}

variable "include_elasticache" {
  type        = bool
  default     = false
  description = "Include the managed Valkey cache actions (modules/valkey): the ElastiCache replication group and subnet group. Set from the env's enable_valkey so the manifest only demands them when the plan provisions the cache. The cache's own security group rides on networking_actions."
}
