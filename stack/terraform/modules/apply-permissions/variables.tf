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

variable "include_metrics_lambda" {
  type        = bool
  default     = false
  description = "Include the metrics-poller lambda, its CloudWatch dashboard, and EventBridge Scheduler actions. Set from the env's enable_metrics_lambda so the manifest only demands them when the plan provisions the lambda."
}
