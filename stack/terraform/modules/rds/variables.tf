variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "db_name" {
  type        = string
  default     = "truthinstream"
  description = "Initial database name."
}

variable "username" {
  type        = string
  default     = "app"
  description = "Master username."
}

variable "engine_version" {
  type        = string
  default     = "17"
  description = "PostgreSQL major version. Must carry pgvector >= 0.8.0 on RDS."
}

variable "instance_class" {
  type        = string
  description = "RDS instance class (e.g. db.t4g.micro for dev)."
}

variable "allocated_storage" {
  type        = number
  default     = 20
  description = "Initial gp3 storage in GiB."
}

variable "max_allocated_storage" {
  type        = number
  default     = 100
  description = "Storage autoscaling ceiling in GiB."
}

variable "multi_az" {
  type        = bool
  description = "Standby replica in a second AZ."
}

variable "deletion_protection" {
  type        = bool
  description = "Refuse instance deletion until explicitly disabled."
}

variable "skip_final_snapshot" {
  type        = bool
  description = "Skip the final snapshot on destroy (dev only)."
}

variable "backup_retention_days" {
  type        = number
  default     = 7
  description = "Automated backup retention."
}

variable "performance_insights" {
  type        = bool
  default     = false
  description = "Enable Performance Insights (not supported on t4g.micro/small)."
}

variable "private_subnet_ids" {
  type        = list(string)
  description = "Private subnets for the DB subnet group."
}

variable "security_group_id" {
  type        = string
  description = "Security group for the instance."
}

variable "secret_recovery_window_days" {
  type        = number
  default     = 7
  description = "Secrets Manager recovery window. 0 purges immediately (dev only)."
}
