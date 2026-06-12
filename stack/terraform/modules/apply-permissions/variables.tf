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
