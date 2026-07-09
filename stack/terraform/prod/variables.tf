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

variable "domain_name" {
  type        = string
  default     = "jeminforme.fr"
  description = "Apex domain the public TLS certificate covers. The authoritative hosted zone lives in the main account; this env only requests the certificate."
}

variable "media_cors_allowed_origins" {
  type        = list(string)
  default     = []
  description = "Browser origins allowed to PUT/GET media objects directly via presigned URLs. Empty (the default) derives the app origins (https://<domain> + https://www.<domain>) from domain_name; set an explicit list to override."
}

# --- Cost right-sizing baseline (VER-134) ---
# Prod runs a deliberately small, single-AZ baseline to avoid over-provisioning.
# Every reduction below is reversible by raising a variable, never by editing
# code: restore HA by setting nat_gateway_count=2, rds_multi_az=true,
# mq_deployment_mode=CLUSTER_MULTI_AZ (on an mq.m5/mq.m7g host), and removing the
# frontend SPOT strategy. See "Cost-baseline (right-sized prod)" in
# stack/terraform/README.md for the documented choices and scale-up order.

variable "nat_gateway_count" {
  type        = number
  default     = 1
  description = "NAT gateways for private-subnet egress. Baseline is 1 (a single AZ's NAT is the egress SPOF, acceptable for early prod); set 2 for per-AZ HA egress. First HA lever to pull as availability matters."

  validation {
    condition     = var.nat_gateway_count >= 1 && var.nat_gateway_count <= 2
    error_message = "nat_gateway_count must be 1 (cost baseline) or 2 (per-AZ HA)."
  }
}

variable "rds_instance_class" {
  type        = string
  default     = "db.t4g.small"
  description = "RDS instance class. db.t4g.small (2 GiB) is the small baseline with enough memory headroom for PostgreSQL + pgvector; scale up the class before adding Multi-AZ if the bottleneck is CPU/memory."
}

variable "rds_multi_az" {
  type        = bool
  default     = false
  description = "RDS standby replica in a second AZ. Baseline is single-AZ (false) for cost; backups and deletion protection are independent of this and stay on. Set true for failover HA — a key durability lever once prod carries real traffic."
}

variable "log_retention_days" {
  type        = number
  default     = 30
  description = "Finite CloudWatch retention for ECS task logs (days). 30 keeps a month of logs without unbounded storage cost; raise for longer audit windows. Must be a CloudWatch-allowed value (1,3,5,7,14,30,60,90,120,150,180,365,400,545,731,...)."
}

variable "mq_deployment_mode" {
  type        = string
  default     = "SINGLE_INSTANCE"
  description = "Amazon MQ deployment mode. SINGLE_INSTANCE is the cost baseline; CLUSTER_MULTI_AZ (3 nodes) is the HA upgrade and requires an mq.m5/mq.m7g host (not mq.t3), so flip mq_host_instance_type with it."

  validation {
    condition     = contains(["SINGLE_INSTANCE", "CLUSTER_MULTI_AZ"], var.mq_deployment_mode)
    error_message = "mq_deployment_mode must be SINGLE_INSTANCE or CLUSTER_MULTI_AZ."
  }
}

variable "mq_host_instance_type" {
  type        = string
  default     = "mq.t3.micro"
  description = "Amazon MQ broker instance class. mq.t3.micro is the smallest baseline; CLUSTER_MULTI_AZ requires mq.m5.* or mq.m7g.*."
}

variable "backend_cpu" {
  type        = number
  default     = 512
  description = "Fargate CPU units for the backend service (1024 = 1 vCPU). 512 right-sizes the serving task for the early-prod baseline."
}

variable "backend_memory" {
  type        = number
  default     = 1024
  description = "Fargate memory in MiB for the backend service."
}

variable "backend_desired_count" {
  type        = number
  default     = 1
  description = "Backend running tasks. Baseline is 1; the backend serves live WebSocket sessions, so it stays on on-demand FARGATE (no SPOT). Raise to 2 for serving redundancy — a primary scale-up lever."
}

variable "frontend_cpu" {
  type        = number
  default     = 256
  description = "Fargate CPU units for the frontend service (1024 = 1 vCPU)."
}

variable "frontend_memory" {
  type        = number
  default     = 512
  description = "Fargate memory in MiB for the frontend service."
}

variable "frontend_desired_count" {
  type        = number
  default     = 1
  description = "Frontend running tasks. Baseline is 1; the frontend is stateless and runs on FARGATE_SPOT when frontend_use_spot is true."
}

variable "frontend_use_spot" {
  type        = bool
  default     = true
  description = "Run the stateless frontend on FARGATE_SPOT (cheaper, interruptible). Safe because the frontend holds no long-lived connection state; set false to pin it to on-demand FARGATE for interruption-free rollouts."
}

variable "enable_rds" {
  type        = bool
  default     = true
  description = "Provision the RDS PostgreSQL instance and wire its DATABASE_URL into the app stack. Defaults true in prod (the production database is managed by RDS). Setting false gates RDS and its DB-dependent consumers (migration task, embedding worker) off."
}

variable "enable_bastion" {
  type        = bool
  default     = false
  description = "Provision the SSM-only bastion used for the one-time embedded-DB load into RDS (scripts/db-tunnel.sh + scripts/db-push.sh). Default false: it is a running instance with a cost, so enable it only for the duration of the load, then disable it. When true its security group is allowed to reach the private RDS on 5432."
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

variable "enable_crawl_worker" {
  type        = bool
  default     = false
  description = "Create the crawl-worker service (crawlworker), mirroring embedworker for the category-crawl ingestion path. Drains the crawl queue and upserts embedded chunks, so it requires enable_rds, and like embedworker it runs under an EXTERNAL deployment controller that prod does not provision (dev's on-demand ingestion moved to EC2 hosts), so it stays foundation-only until one is added. Default false."
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

variable "enable_crawl_producer" {
  type        = bool
  default     = false
  description = "Create the on-demand category-crawl producer family (wikicrawl). It walks Wikipedia categories and publishes crawl-chunk jobs; it has no database, so it is gated only on this flag. Default false; launched with `aws ecs run-task`."
}

variable "crawl_producer_cpu" {
  type        = number
  default     = 1024
  description = "Fargate CPU units for the wikicrawl producer task (1024 = 1 vCPU)."
}

variable "crawl_producer_memory" {
  type        = number
  default     = 2048
  description = "Fargate memory in MiB for the wikicrawl producer task."
}

variable "crawl_categories" {
  type        = string
  default     = "Category:Climate change"
  description = "Default comma-separated Wikipedia categories the wikicrawl producer walks (CRAWL_CATEGORIES). Overridable per run via the run-task command."
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
  description = "Create the fact-check-worker service (factcheckworker), draining the factcheck.claims queue and upserting curated claims. Requires enable_rds, and like the other worker fleets it runs under an EXTERNAL deployment controller that prod does not provision (dev's on-demand ingestion moved to EC2 hosts), so it stays foundation-only until one is added. Default false."
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
  description = "Create the scrutins-worker service (scrutinsworker), draining the scrutins.votes queue and upserting vote records. Requires enable_rds, and like the other worker fleets it runs under an EXTERNAL deployment controller that prod does not provision (dev's on-demand ingestion moved to EC2 hosts), so it stays foundation-only until one is added. Default false."
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

variable "enable_valkey" {
  type        = bool
  default     = true
  description = "Provision the managed Valkey cache and inject REDIS_URL into the backend. On by default in prod so the 24h analysis cache is live; turning it off falls the backend back to the no-op cache (empty REDIS_URL)."
}

variable "valkey_node_type" {
  type        = string
  default     = "cache.t4g.micro"
  description = "ElastiCache node class for the Valkey cache. A single small node suits a 24h ephemeral cache. The engine version and parameter group are coupled and stay on the module defaults (Valkey 8.x)."
}

# --- Self-hosted Keycloak (VER-156) ---

variable "enable_keycloak" {
  type        = bool
  default     = true
  description = "Provision the self-hosted Keycloak identity provider (ECS Fargate service + its dedicated database on RDS). Requires enable_rds. Defaults true in prod so a tag deploys the identity provider straight away; set false to run Keycloak out of band."
}

variable "keycloak_cpu" {
  type        = number
  default     = 512
  description = "Fargate task CPU units for Keycloak. 512 is the small baseline; Quarkus wants headroom, so raise this (with keycloak_memory) before adding a second node under load."
}

variable "keycloak_memory" {
  type        = number
  default     = 1024
  description = "Fargate task memory (MiB) for Keycloak. 1 GiB is the small baseline; must be a valid Fargate CPU/memory pairing for keycloak_cpu."
}

variable "keycloak_desired_count" {
  type        = number
  default     = 1
  description = "Number of Keycloak tasks. 1 is the cost baseline (single node); raise for identity-provider HA once it carries real traffic."
}

variable "keycloak_admin_username" {
  type        = string
  default     = "admin"
  description = "Keycloak master-realm bootstrap admin username (KC_BOOTSTRAP_ADMIN_USERNAME). Only the password is secret (Terraform-generated, stored in Secrets Manager)."
}

variable "keycloak_db_bootstrap_image" {
  type        = string
  default     = "public.ecr.aws/docker/library/postgres:17-alpine"
  description = "Image providing the psql client for the keycloak DB-bootstrap task. Defaults to the official Postgres 17 image (matches the RDS major) from the AWS public ECR mirror, so no bespoke image build is needed."
}

variable "enable_legacy_password_login" {
  type        = bool
  default     = false
  description = "Re-enable the retired operator password login (/api/login + /api/logout). Off by default: the /api gate uses the verified Keycloak identity. When true, the AUTH_EMAIL/AUTH_PASSWORD_HASH/SESSION_SECRET secret containers are created (values pushed out of band) and injected into the backend along with LEGACY_PASSWORD_LOGIN=true."
}
