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
  default     = false
  description = "Provision the RDS PostgreSQL instance and wire its DATABASE_URL into the app stack. Default false in dev: the database is developed locally, so dev creates no RDS and the DB-dependent migration task and embedding worker stay gated off (they require enable_rds). Set true to bring the managed database online."
}

variable "enable_bastion" {
  type        = bool
  default     = false
  description = "Provision the SSM-only bastion for the local-worker tunnel (scripts/ssm-port-forward.sh). Default false: it is a running instance with a cost, so enable it only for the duration of a corpus ingest, then disable it. When true its security group is allowed to reach the broker on AMQPS."
}

variable "bastion_instance_type" {
  type        = string
  default     = "t3.micro"
  description = "Bastion instance class (x86_64 family; the AMI is x86_64). t3.micro suffices: the host only relays SSM port-forward traffic, it runs no workload."
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

variable "enable_db_backup" {
  type        = bool
  default     = false
  description = "Create the scheduled pg_dump-to-S3 backup task. Keep false until the backup image ships; flipping it is a no-op on running services."
}

variable "enable_producer" {
  type        = bool
  default     = false
  description = "Create the on-demand embedding-queue producer task. Requires enable_rds (the producer stages and reads corpus chunks from the database). Launch it with `aws ecs run-task` once enabled; keep false until a corpus run is wanted."
}

variable "producer_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the producer task (1024 = 1 vCPU)."
}

variable "producer_memory" {
  type        = number
  default     = 4096
  description = "Fargate memory in MiB for the producer task; sized for the dump ingest before publishing."
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

variable "enable_metrics_lambda" {
  type        = bool
  default     = false
  description = "Provision the scheduled metrics-poller lambda and the ingestion CloudWatch dashboard. The lambda polls the broker's RabbitMQ management API for per-queue stats. Default false; enable it alongside the broker and worker. Requires `make lambda-mqmetrics` in stack/backend to have built the bootstrap binary before apply."
}

variable "metrics_namespace" {
  type        = string
  default     = "TruthInStream/RabbitMQ"
  description = "Custom CloudWatch namespace the metrics lambda publishes to and the dashboard reads from."
}

variable "metrics_poll_schedule" {
  type        = string
  default     = "rate(1 minute)"
  description = "EventBridge Scheduler expression for how often the metrics lambda polls the broker."
}
