variable "aws_region" {
  type        = string
  default     = "eu-west-3"
  description = "AWS region for all resources."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."

  validation {
    condition     = contains(["dev", "prod"], var.environment)
    error_message = "environment must be dev or prod."
  }
}

variable "media_cors_allowed_origins" {
  type        = list(string)
  default     = ["*"]
  description = "Browser origins allowed to PUT/GET media objects directly via presigned URLs. Defaults to any origin while there is no fixed frontend domain; restrict to the app origin once one exists."
}

variable "enable_rds" {
  type        = bool
  default     = true
  description = "Provision the RDS PostgreSQL instance and wire its DATABASE_URL into the app stack. Defaults true in prod (the production database is managed by RDS). Setting false gates RDS and its DB-dependent consumers (migration task, embedding worker) off."
}

variable "enable_wiki_sync" {
  type        = bool
  default     = false
  description = "Create the scheduled Wikipedia delta-sync task. Requires enable_rds (the sync writes to the database). Keep false until the wikisync binary ships in the backend image."
}

variable "wiki_corpus" {
  type        = string
  default     = "simplewiki"
  description = "Wikipedia corpus the sync targets (e.g. simplewiki, enwiki). See modules/scheduled-task/README.md for RDS sizing before enwiki."
}

variable "wiki_sync_schedule" {
  type        = string
  default     = "cron(0 3 ? * SUN *)"
  description = "EventBridge Scheduler expression for the weekly delta sync (UTC)."
}

variable "wiki_sync_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the wikisync task (1024 = 1 vCPU)."
}

variable "wiki_sync_memory" {
  type        = number
  default     = 4096
  description = "Fargate memory in MiB for the wikisync task."
}
variable "enable_embed_worker" {
  type        = bool
  default     = false
  description = "Create the embedding-worker service. Default false; enable it (and set embed_worker_desired_count) when running a corpus ingest that publishes embedding jobs."
}

variable "embed_worker_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units per worker replica (1024 = 1 vCPU)."
}

variable "embed_worker_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB per worker replica."
}

variable "embed_worker_desired_count" {
  type        = number
  default     = 2
  description = "Number of worker replicas. Scale this to scale embedding throughput."
}

variable "embed_worker_concurrency" {
  type        = number
  default     = 4
  description = "Jobs one replica embeds in parallel (EMBED_WORKER_CONCURRENCY)."
}

variable "embed_worker_max_attempts" {
  type        = number
  default     = 5
  description = "Per-job delivery budget before a persistent failure is dropped with a log (EMBED_WORKER_MAX_ATTEMPTS)."
}

variable "enable_crawl_worker" {
  type        = bool
  default     = false
  description = "Create the crawl-worker service (crawlworker), mirroring embedworker for the category-crawl ingestion path. Drains the crawl queue and upserts embedded chunks, so it requires enable_rds, and like embedworker it runs under an EXTERNAL deployment controller that prod does not yet provision (no worker-lifecycle module here), so it stays foundation-only until that is added. Default false."
}

variable "crawl_worker_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units per crawl-worker replica (1024 = 1 vCPU)."
}

variable "crawl_worker_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB per crawl-worker replica."
}

variable "crawl_worker_desired_count" {
  type        = number
  default     = 2
  description = "Number of crawl-worker replicas. Scale this to scale crawl embedding throughput."
}

variable "crawl_worker_concurrency" {
  type        = number
  default     = 4
  description = "Jobs one crawl-worker replica embeds in parallel (CRAWL_WORKER_CONCURRENCY)."
}

variable "crawl_worker_max_attempts" {
  type        = number
  default     = 5
  description = "Per-job delivery budget before a persistent failure is dropped with a log (CRAWL_WORKER_MAX_ATTEMPTS)."
}

variable "enable_db_backup" {
  type        = bool
  default     = false
  description = "Create the scheduled pg_dump-to-S3 backup task. Keep false until the backup image ships; flipping it is a no-op on running services."
}

variable "db_backup_schedule" {
  type        = string
  default     = "cron(0 4 * * ? *)"
  description = "EventBridge Scheduler expression for the database backup (UTC); default is daily at 04:00."
}

variable "db_backup_cpu" {
  type        = number
  default     = 512
  description = "Fargate CPU units for the backup task (1024 = 1 vCPU)."
}

variable "db_backup_memory" {
  type        = number
  default     = 1024
  description = "Fargate memory in MiB for the backup task."
}
