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
  description = "Provision the RDS PostgreSQL instance and wire its DATABASE_URL into the app stack. Default false in dev: the database is developed locally, so dev creates no RDS and the DB-dependent migration task stays gated off (it requires enable_rds). Set true to bring the managed database online."
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

variable "enable_ingestion_hosts" {
  type        = bool
  default     = false
  description = "Provision the two on-demand EC2 ingestion hosts (a crawler host running the producers and a consumer host running the worker fleets), each SSM-only (no inbound, no SSH), IMDSv2-required, with an instance profile scoped to SSM core + the specific ingestion secrets + backend ECR pull + CloudWatch Logs. Default false: they are running instances with a cost, so enable them only for an ingestion run, then stop them to zero cost. Enabling them implies enable_rds (the consumer writes the corpus to the managed database), and their security groups are admitted by the broker on 5671 and RDS on 5432. Note: to pause an ingestion run, STOP the instances (aws ec2 stop-instances) rather than flipping this back to false - because it implies enable_rds, disabling it tears the dev RDS instance down (skip_final_snapshot = true), discarding the ingested corpus. Set enable_rds = true independently first if the database must outlive the hosts."
}

variable "crawler_host_instance_type" {
  type        = string
  default     = "t3.small"
  description = "Crawler-host instance class (x86_64 family; the AMI is x86_64). t3.small suits the producers, which stream external APIs and publish to the queue without heavy local compute."
}

variable "consumer_host_instance_type" {
  type        = string
  default     = "t3.medium"
  description = "Consumer-host instance class (x86_64 family; the AMI is x86_64). t3.medium is larger than the crawler because the worker fleets embed and upsert in parallel; raise it to scale drain throughput."
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

variable "enable_metrics_lambda" {
  type        = bool
  default     = false
  description = "Provision the scheduled metrics-poller lambda and the ingestion CloudWatch dashboard. The lambda polls the broker's RabbitMQ management API for per-queue stats, which the `/consumer status` action reads. Default false; enable it alongside the broker and the ingestion hosts. Requires `make lambda-mqmetrics` in stack/backend to have built the bootstrap binary before apply."
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
