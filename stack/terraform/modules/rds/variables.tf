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

variable "iops" {
  type        = number
  default     = null
  description = "Provisioned gp3 IOPS. Null (the default) leaves the volume on the gp3 baseline (3000 IOPS). RDS gp3 only accepts a provisioned value above the baseline once allocated_storage reaches the 400 GiB threshold, so keep this null on small instances (e.g. dev)."
}

variable "storage_throughput" {
  type        = number
  default     = null
  description = "Provisioned gp3 storage throughput in MiB/s. Null (the default) leaves the volume on the gp3 baseline (125 MiB/s). Like iops, a value above the baseline is only valid once allocated_storage reaches the 400 GiB gp3 threshold; keep null on small instances."
}

variable "parameter_group_name" {
  type        = string
  default     = null
  description = "Name of a custom DB parameter group to attach. Null (the default) leaves the instance on the engine's default parameter group. The prod root passes a pgvector-tuned group here; dev stays on the default."
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
