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

variable "enable_crawl_producer" {
  type        = bool
  default     = false
  description = "Create the on-demand category-crawl producer task (wikicrawl). Unlike the dump producer it is database-free - it crawls the MediaWiki API, runs the fact-checkability gate, and publishes self-contained chunk jobs to the crawl queue - so it does not require enable_rds. Launch it with `aws ecs run-task`, overriding CRAWL_CATEGORIES per run; keep false until a crawl run is wanted."
}

variable "crawl_producer_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the crawl producer task (1024 = 1 vCPU)."
}

variable "crawl_producer_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB for the crawl producer task; it streams the API and gates chunks without staging a dump, so it is lighter than the dump producer."
}

variable "crawl_categories" {
  type        = string
  default     = "Category:Climate change"
  description = "Default CRAWL_CATEGORIES the crawl producer task definition carries; override per run with `aws ecs run-task --overrides` to crawl a different slice. Note: the fact-checkability gate is on by default (CRAWL_CHECKWORTHY=true), so the producer also needs the checkworthy-api-key secret populated out of band before a run, or CRAWL_CHECKWORTHY=false to disable the gate."
}

variable "enable_crawl_worker" {
  type        = bool
  default     = false
  description = "Create the crawl-worker service (crawlworker). Drains the crawl queue and upserts embedded chunks into the live corpus, so it requires enable_rds. Like embedworker it runs under the worker-lifecycle EXTERNAL controller and stays at zero until its scaling max is raised. Default false; enable it alongside a crawl run."
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

variable "enable_factcheck_producer" {
  type        = bool
  default     = false
  description = "Create the on-demand fact-check producer family (factcheckcrawl). It reads the Google Fact Check Tools API and publishes curated-claim jobs; it has no database, so it is gated only on this flag. Default false; launched with `aws ecs run-task`."
}

variable "factcheck_producer_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the factcheckcrawl producer task (1024 = 1 vCPU)."
}

variable "factcheck_producer_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB for the factcheckcrawl producer task."
}

variable "factcheck_queries" {
  type        = string
  default     = "désinformation"
  description = "Default comma-separated Fact Check Tools queries the factcheckcrawl producer runs (FACTCHECK_QUERIES). Overridable per run via the run-task command."
}

variable "enable_scrutins_producer" {
  type        = bool
  default     = false
  description = "Create the on-demand scrutins producer family (scrutinscrawl). It downloads the Assemblee Nationale open-data archive and publishes scrutin jobs; it has no database and no secret, so it is gated only on this flag. Default false; launched with `aws ecs run-task`."
}

variable "scrutins_producer_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the scrutinscrawl producer task (1024 = 1 vCPU)."
}

variable "scrutins_producer_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB for the scrutinscrawl producer task."
}

variable "enable_factcheck_worker" {
  type        = bool
  default     = false
  description = "Create the fact-check-worker service (factcheckworker), draining the factcheck.claims queue and upserting curated claims. Requires enable_rds; runs under an EXTERNAL deployment controller, so the worker-lifecycle lambda owns its task sets and scale via the factcheckworker entry in worker_lifecycle_scaling_config. Default false."
}

variable "factcheck_worker_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units per fact-check-worker replica (1024 = 1 vCPU)."
}

variable "factcheck_worker_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB per fact-check-worker replica."
}

variable "factcheck_worker_desired_count" {
  type        = number
  default     = 2
  description = "Number of fact-check-worker replicas. Scale this to scale fact-check ingestion throughput."
}

variable "factcheck_worker_concurrency" {
  type        = number
  default     = 4
  description = "Jobs one fact-check-worker replica processes in parallel (CRAWL_WORKER_CONCURRENCY, the shared worker tuning the binary reads)."
}

variable "factcheck_worker_max_attempts" {
  type        = number
  default     = 5
  description = "Per-job delivery budget before a persistent failure is dropped with a log (CRAWL_WORKER_MAX_ATTEMPTS, the shared worker tuning the binary reads)."
}

variable "enable_scrutins_worker" {
  type        = bool
  default     = false
  description = "Create the scrutins-worker service (scrutinsworker), draining the scrutins.votes queue and upserting vote records. Requires enable_rds; runs under an EXTERNAL deployment controller, so the worker-lifecycle lambda owns its task sets and scale via the scrutinsworker entry in worker_lifecycle_scaling_config. Default false."
}

variable "scrutins_worker_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units per scrutins-worker replica (1024 = 1 vCPU)."
}

variable "scrutins_worker_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB per scrutins-worker replica."
}

variable "scrutins_worker_desired_count" {
  type        = number
  default     = 2
  description = "Number of scrutins-worker replicas. Scale this to scale scrutins ingestion throughput."
}

variable "scrutins_worker_concurrency" {
  type        = number
  default     = 4
  description = "Jobs one scrutins-worker replica processes in parallel (SCRUTINS_WORKER_CONCURRENCY)."
}

variable "scrutins_worker_max_attempts" {
  type        = number
  default     = 5
  description = "Per-job delivery budget before a persistent failure is dropped with a log (SCRUTINS_WORKER_MAX_ATTEMPTS)."
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

variable "enable_worker_lifecycle" {
  type        = bool
  default     = false
  description = "Provision the worker-lifecycle lambda (queue-depth autoscaling and zero-downtime rollout for the embedding-worker fleet). Default false; enable it alongside the broker and worker. Requires `make lambda-workerlifecycle` in stack/backend to have built the bootstrap binary before apply. The fleet stays at zero until a service's scaling max is raised above zero."
}

variable "worker_lifecycle_scaling_config" {
  type = map(object({
    queue_base       = string
    ratio            = number
    min              = number
    max              = number
    cooldown_seconds = number
  }))
  default = {
    embedworker = {
      queue_base       = "embedding.jobs"
      ratio            = 200
      min              = 0
      max              = 0
      cooldown_seconds = 180
    }
    crawlworker = {
      queue_base       = "crawl.chunks"
      ratio            = 200
      min              = 0
      max              = 0
      cooldown_seconds = 180
    }
    factcheckworker = {
      queue_base       = "factcheck.claims"
      ratio            = 200
      min              = 0
      max              = 0
      cooldown_seconds = 180
    }
    scrutinsworker = {
      queue_base       = "scrutins.votes"
      ratio            = 200
      min              = 0
      max              = 0
      cooldown_seconds = 180
    }
  }
  description = "Per-service queue-depth scaling policy for the worker-lifecycle lambda, keyed by ECS service name. max = 0 keeps a service disabled - the gate that holds the worker fleets at zero until they move onto ECS. Raise a service's max to enable its autoscaling (embedworker drains embedding.jobs, crawlworker drains crawl.chunks, factcheckworker drains factcheck.claims, scrutinsworker drains scrutins.votes)."
}
