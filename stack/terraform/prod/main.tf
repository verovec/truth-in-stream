data "aws_caller_identity" "current" {}

locals {
  project           = "truth-in-stream"
  github_repository = "verovec/truth-in-stream"

  # DATABASE_URL secret ARN, or null when RDS is gated off. DB consumers read
  # this: the backend drops the secret and the migration task / embedding worker
  # gate themselves off when it is null. Prod runs RDS by default.
  rds_dsn_secret_arn = one(module.rds[*].dsn_secret_arn)

  # Backend REDIS_URL, or null when the Valkey cache is gated off. The URL is a
  # rediss://endpoint:6379 with TLS but no auth token, so it carries no secret and
  # rides on the backend's plain environment_variables (not Secrets Manager); the
  # card explicitly sanctions an env var here (no auth token). An empty/absent
  # REDIS_URL makes the backend fall back to its no-op cache.
  redis_url = one(module.valkey[*].redis_url)
}

# Public TLS certificate for jeminforme.fr (apex + www), requested in us-east-1
# for CloudFront. DNS validation records are NOT created here: the authoritative
# hosted zone is in the main account (040265332493), so the main-account
# terraform root (VER-140) creates the records by reading this module's
# domain_validation_options output. The certificate is PENDING_VALIDATION until
# those records exist; that does not block this plan.
module "acm" {
  source = "../modules/acm"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  domain_name               = var.domain_name
  subject_alternative_names = ["www.${var.domain_name}"]
}

module "vpc" {
  source = "../modules/vpc"

  project     = local.project
  environment = var.environment
  # Cost-baseline: a single NAT gateway (one AZ's NAT is the egress SPOF). Set
  # nat_gateway_count = 2 to restore per-AZ HA egress. See the cost-baseline note
  # in stack/terraform/README.md.
  nat_gateway_count = var.nat_gateway_count

  # The SSM bastion reaches the private RDS on 5432 for the one-time DB load.
  # Its SG joins the postgres SG's allow-list only when the bastion is
  # provisioned (an inline rule on the postgres SG, reusing the broker SG's
  # pattern; a standalone rule would conflict with that SG's inline ingress
  # under provider v6, and routing the SG back here is acyclic because the
  # bastion SG depends only on the VPC, not on the postgres SG).
  database_client_security_group_ids = var.enable_bastion ? [module.bastion[0].security_group_id] : []
}

module "ecs" {
  source = "../modules/ecs"

  project     = local.project
  environment = var.environment
  # Finite log retention so task logs do not accumulate forever (cost baseline).
  log_retention_days = var.log_retention_days
}

module "ecr" {
  source = "../modules/ecr"

  project      = local.project
  environment  = var.environment
  repositories = ["backend", "frontend", "migrate", "backup"]
}

module "rds" {
  source = "../modules/rds"
  count  = var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment

  # Cost-baseline: a small single-AZ instance. Backups (21-day retention),
  # deletion protection, and the final snapshot are independent of Multi-AZ and
  # stay on; only the standby replica is dropped. Set rds_multi_az = true (and
  # scale rds_instance_class as needed) to restore failover HA.
  instance_class        = var.rds_instance_class
  multi_az              = var.rds_multi_az
  deletion_protection   = true
  skip_final_snapshot   = false
  backup_retention_days = 21

  private_subnet_ids = module.vpc.private_subnet_ids
  security_group_id  = module.vpc.postgres_security_group_id
}

# Message broker for the embedding-job queue. Cost-baseline: a single-instance
# mq.t3.micro broker. Promoting to CLUSTER_MULTI_AZ for HA is a deliberate
# follow-up once the worker fleet drives production traffic — set
# mq_deployment_mode = CLUSTER_MULTI_AZ together with an mq.m5/mq.m7g
# mq_host_instance_type (mq.t3 does not support clustering). A SINGLE_INSTANCE
# broker takes exactly one subnet; a CLUSTER_MULTI_AZ broker takes exactly two
# (Amazon MQ rejects any other count), so the cluster path slices the private
# subnets to the first two regardless of az_count.
module "rabbitmq" {
  source = "../modules/rabbitmq"

  project     = local.project
  environment = var.environment

  deployment_mode    = var.mq_deployment_mode
  host_instance_type = var.mq_host_instance_type

  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = var.mq_deployment_mode == "CLUSTER_MULTI_AZ" ? slice(module.vpc.private_subnet_ids, 0, 2) : [module.vpc.private_subnet_ids[0]]
  allowed_security_group_ids = [module.vpc.ecs_tasks_security_group_id]
}

# Managed Valkey cache for the 24h analysis-replay cache (VER-145). A single
# small node in the private subnets, reachable only from the backend task SG on
# the Redis port. Valkey is wire-compatible with the go-redis client, so the
# backend needs no change; it reads the endpoint as REDIS_URL below. Gated by
# enable_valkey (on in prod). A node failure causes only cache misses, never data
# loss, so a single node with no replica is the right cost/HA trade for an
# ephemeral cache.
module "valkey" {
  source = "../modules/valkey"
  count  = var.enable_valkey ? 1 : 0

  project     = local.project
  environment = var.environment

  vpc_id                     = module.vpc.vpc_id
  private_subnet_ids         = module.vpc.private_subnet_ids
  allowed_security_group_ids = [module.vpc.ecs_tasks_security_group_id]

  node_type = var.valkey_node_type
}

# SSM-only bastion for the one-time embedded-DB load into RDS: an operator opens
# a port-forward through it to the private database (`make db-tunnel`, then
# `make db-push`) and loads the local pg_dump over the tunnel. No public IP, no
# SSH, no inbound rules; its SG is allowed to reach RDS on 5432 (wired into the
# vpc module's database_client_security_group_ids above). Gated off by default
# (it is a running instance with a cost); enable it only for the load, then
# disable it.
module "bastion" {
  source = "../modules/bastion"
  count  = var.enable_bastion ? 1 : 0

  project     = local.project
  environment = var.environment

  vpc_id        = module.vpc.vpc_id
  subnet_id     = module.vpc.private_subnet_ids[0]
  instance_type = var.bastion_instance_type
}

# Application runtime secrets. Terraform creates the containers only (no values);
# `make push-secrets ENV=prod` (scripts/push-secrets.sh) reads the allowlisted
# keys from the local .env and writes the values out of band. The set here is the
# single source of truth for that allowlist — adding a key means adding a resource
# here AND wiring it into the consuming task def's `secrets` below. The
# terraform-owned DATABASE_URL (rds) and RABBITMQ_URL (rabbitmq) secrets are NOT
# in this set: they are generated by terraform and must never be pushed from .env.
resource "aws_secretsmanager_secret" "embedding_api_key" {
  name        = "${local.project}/${var.environment}/app/embedding-api-key"
  description = "Voyage AI API key. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "transcription_api_key" {
  name        = "${local.project}/${var.environment}/app/transcription-api-key"
  description = "AssemblyAI API key. Value set manually, never in Terraform."
}

# Operator auth: the single-operator login and the session-cookie signing secret.
# The backend's config requires all three at start (requireEnv), so the serving
# task cannot run without them.
resource "aws_secretsmanager_secret" "auth_email" {
  name        = "${local.project}/${var.environment}/app/auth-email"
  description = "Operator login email. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "auth_password_hash" {
  name        = "${local.project}/${var.environment}/app/auth-password-hash"
  description = "Operator argon2id password hash. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "session_secret" {
  name        = "${local.project}/${var.environment}/app/session-secret"
  description = "Session-cookie signing secret. Value set manually, never in Terraform."
}

# LLM gate provider keys. Optional at runtime (the checkworthy/digest gate reads
# whichever matches LLM_PROVIDER); declared so the operator can populate the one
# in use without a terraform change.
resource "aws_secretsmanager_secret" "deepseek_api_key" {
  name        = "${local.project}/${var.environment}/app/deepseek-api-key"
  description = "DeepSeek LLM API key. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "gemini_api_key" {
  name        = "${local.project}/${var.environment}/app/gemini-api-key"
  description = "Google Gemini LLM API key. Value set manually, never in Terraform."
}

# Slack incoming webhook for crawl/run alerts. Optional; alerts are skipped when
# unset.
resource "aws_secretsmanager_secret" "slack_webhook_url" {
  name        = "${local.project}/${var.environment}/app/slack-webhook-url"
  description = "Slack incoming webhook URL. Value set manually, never in Terraform."
}

# Anthropic API key for the category-crawl producer's fact-checkability gate
# (CHECKWORTHY_API_KEY). Created empty; the value is set out of band. Consumed by
# the wikicrawl producer to judge whether each crawled chunk is citable evidence
# before it is published.
resource "aws_secretsmanager_secret" "checkworthy_api_key" {
  name        = "${local.project}/${var.environment}/app/checkworthy-api-key"
  description = "Anthropic API key for the crawl fact-checkability gate. Value set manually, never in Terraform."
}

# Google Fact Check Tools API key for the fact-check producer (FACTCHECK_API_KEY).
# Created empty; the value is set out of band. Consumed by the factcheckcrawl
# producer to read already-checked French claims from the aggregator API.
resource "aws_secretsmanager_secret" "factcheck_api_key" {
  name        = "${local.project}/${var.environment}/app/factcheck-api-key"
  description = "Google Fact Check Tools API key for the fact-check crawl producer. Value set manually, never in Terraform."
}

module "media_storage" {
  source = "../modules/s3"

  project     = local.project
  environment = var.environment

  cors_allowed_origins = var.media_cors_allowed_origins
}

# Durable home for pg_dump snapshots, so the embedded corpus survives a loss
# without re-embedding. Written by `make backup` with operator credentials and,
# when enabled, by the scheduled db_backup task below.
module "db_backup_storage" {
  source = "../modules/s3-backup"

  project     = local.project
  environment = var.environment
}

module "iam" {
  source = "../modules/iam"

  project           = local.project
  environment       = var.environment
  github_repository = local.github_repository
  # main for terraform-apply on merge and manual workflow_dispatch deploys;
  # refs/tags/v* for the tag-triggered prod release (release.yml). Both are
  # human-gated production gestures. Never widen to a blanket wildcard.
  github_deploy_refs = ["refs/heads/main", "refs/tags/v*"]
  # The OIDC provider is account-global and owned by the dev environment.
  create_oidc_provider = false

  ecr_repository_arns  = module.ecr.repository_arns
  cluster_arn          = module.ecs.cluster_id
  media_bucket_arn     = module.media_storage.bucket_arn
  db_backup_bucket_arn = module.db_backup_storage.bucket_arn
  secret_arns = concat(
    local.rds_dsn_secret_arn != null ? [local.rds_dsn_secret_arn] : [],
    [
      aws_secretsmanager_secret.embedding_api_key.arn,
      aws_secretsmanager_secret.transcription_api_key.arn,
      aws_secretsmanager_secret.auth_email.arn,
      aws_secretsmanager_secret.auth_password_hash.arn,
      aws_secretsmanager_secret.session_secret.arn,
      aws_secretsmanager_secret.deepseek_api_key.arn,
      aws_secretsmanager_secret.gemini_api_key.arn,
      aws_secretsmanager_secret.slack_webhook_url.arn,
      aws_secretsmanager_secret.checkworthy_api_key.arn,
      aws_secretsmanager_secret.factcheck_api_key.arn,
      module.rabbitmq.url_secret_arn,
    ],
  )
  ssm_parameter_arns = [
    aws_ssm_parameter.private_subnet_ids.arn,
    aws_ssm_parameter.tasks_security_group_id.arn,
  ]
}

# Internal ALB: no public DNS, reachable only from CloudFront through the VPC
# origin below. The module owns a security group restricted to the CloudFront
# origin-facing prefix list, so nothing else in the VPC can reach it either.
module "alb" {
  source = "../modules/alb"

  project     = local.project
  environment = var.environment

  internal            = true
  vpc_id              = module.vpc.vpc_id
  private_subnet_ids  = module.vpc.private_subnet_ids
  deletion_protection = true
  # No certificate on the ALB itself: TLS terminates at CloudFront and the
  # VPC-origin hop to the internal ALB stays on plain HTTP inside the VPC.
}

# CLOUDFRONT-scoped WAFv2 web ACL, created in us-east-1 (the region CloudFront
# web ACLs must live in, same constraint as the cert). Runs AWS managed rule
# groups plus a per-IP rate throttle and logs every decision to CloudWatch with
# the Authorization and Cookie headers redacted. Associated with the
# distribution below via web_acl_id.
module "waf" {
  source = "../modules/waf"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  project     = local.project
  environment = var.environment
}

# CloudFront in front of the internal ALB. Serves the app over HTTPS at the
# apex and www using the us-east-1 ACM certificate from the acm module, with a
# default behavior (frontend) and an /api/* behavior (backend), both dynamic
# and never cached. Media is served from direct presigned S3 URLs and is not
# fronted here. The WAFv2 web ACL above is associated via web_acl_id; the
# apex/www alias records are created by the main-account root (VER-140), which
# reads domain_name and hosted_zone_id.
module "cloudfront" {
  source = "../modules/cloudfront"

  project     = local.project
  environment = var.environment

  alb_arn         = module.alb.arn
  alb_dns_name    = module.alb.dns_name
  certificate_arn = module.acm.certificate_arn
  aliases         = [var.domain_name, "www.${var.domain_name}"]
  web_acl_id      = module.waf.web_acl_arn
}

module "backend" {
  source = "../modules/service"

  project     = local.project
  environment = var.environment
  name        = "backend"

  image          = "${module.ecr.repository_urls["backend"]}:latest"
  container_port = 8080
  # Right-sized serving task. The backend terminates live AssemblyAI/transcription
  # WebSocket sessions, so it stays on on-demand FARGATE (no SPOT) — a SPOT
  # reclamation would drop in-flight live sessions. Raise backend_desired_count
  # for serving redundancy.
  cpu           = var.backend_cpu
  memory        = var.backend_memory
  desired_count = var.backend_desired_count

  environment_variables = merge(
    {
      PORT = "8080"
      # Object storage uses the task role for credentials (no endpoint, no static
      # keys); only the bucket and region are configured here.
      STORAGE_BUCKET = module.media_storage.bucket_id
      STORAGE_REGION = var.aws_region
    },
    # REDIS_URL turns on the 24h analysis cache. No auth token, so it is a plain
    # env var, not a secret; absent when the Valkey cache is gated off.
    local.redis_url != null ? { REDIS_URL = local.redis_url } : {},
  )
  secrets = merge(
    {
      EMBEDDING_API_KEY     = aws_secretsmanager_secret.embedding_api_key.arn
      TRANSCRIPTION_API_KEY = aws_secretsmanager_secret.transcription_api_key.arn
      AUTH_EMAIL            = aws_secretsmanager_secret.auth_email.arn
      AUTH_PASSWORD_HASH    = aws_secretsmanager_secret.auth_password_hash.arn
      SESSION_SECRET        = aws_secretsmanager_secret.session_secret.arn
      DEEPSEEK_API_KEY      = aws_secretsmanager_secret.deepseek_api_key.arn
      GEMINI_API_KEY        = aws_secretsmanager_secret.gemini_api_key.arn
      SLACK_WEBHOOK_URL     = aws_secretsmanager_secret.slack_webhook_url.arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_id       = module.vpc.ecs_tasks_security_group_id
  alb_security_group_id   = module.alb.security_group_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name

  vpc_id                 = module.vpc.vpc_id
  listener_arn           = module.alb.listener_arn
  listener_rule_priority = 10
  path_patterns          = ["/api/*", "/healthz"]
  health_check_path      = "/healthz"
}

module "frontend" {
  source = "../modules/service"

  project     = local.project
  environment = var.environment
  name        = "frontend"

  image          = "${module.ecr.repository_urls["frontend"]}:latest"
  container_port = 3000
  # Right-sized stateless frontend. It holds no long-lived connection state, so
  # it runs on FARGATE_SPOT (cheaper, interruptible) when frontend_use_spot is
  # true; set it false to pin to on-demand FARGATE.
  cpu           = var.frontend_cpu
  memory        = var.frontend_memory
  desired_count = var.frontend_desired_count
  capacity_provider_strategy = var.frontend_use_spot ? [{
    capacity_provider = "FARGATE_SPOT"
    weight            = 1
    base              = 0
  }] : []

  environment_variables = {
    PORT                    = "3000"
    NEXT_TELEMETRY_DISABLED = "1"
  }

  cluster_id              = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_id       = module.vpc.ecs_tasks_security_group_id
  alb_security_group_id   = module.alb.security_group_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name

  vpc_id                 = module.vpc.vpc_id
  listener_arn           = module.alb.listener_arn
  listener_rule_priority = 100
  path_patterns          = ["/*"]
  # The auth proxy 307s cookie-less requests to /login, which serves 200.
  # Ordering: deploy a frontend image that serves /login BEFORE applying this
  # change; the pre-auth image 404s it and the target group would go unhealthy.
  health_check_path = "/login"
}

# The migration task runs golang-migrate against RDS, so it only exists when RDS
# does (enable_rds, default true in prod).
module "migration" {
  source = "../modules/migration"
  count  = var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment

  image                   = "${module.ecr.repository_urls["migrate"]}:latest"
  dsn_secret_arn          = local.rds_dsn_secret_arn
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Weekly Wikipedia delta sync as a one-shot scheduled Fargate task.
module "wiki_sync" {
  source = "../modules/scheduled-task"
  # Writes to the database, so it requires RDS as well as its own enable flag.
  count = var.enable_wiki_sync && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "wikisync"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/wikisync"]
  command     = ["-mode=delta"]

  schedule_expression = var.wiki_sync_schedule
  cpu                 = var.wiki_sync_cpu
  memory              = var.wiki_sync_memory

  environment_variables = {
    WIKI_CORPUS = var.wiki_corpus
  }
  secrets = merge(
    { EMBEDDING_API_KEY = aws_secretsmanager_secret.embedding_api_key.arn },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Scheduled database backup: a one-shot Fargate task that runs pg_dump against
# RDS and uploads the dump to the backup bucket on a cron, so a snapshot exists
# without anyone running `make backup`. Gated off by default; flip
# enable_db_backup once the backup image ships and the schedule is wanted.
module "db_backup" {
  source = "../modules/scheduled-task"
  # Dumps RDS, so it requires RDS as well as its own enable flag.
  count = var.enable_db_backup && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "dbbackup"

  image = "${module.ecr.repository_urls["backup"]}:latest"
  # The image's ENTRYPOINT is the dbbackup binary; keep it and pass no command.
  entry_point = []
  command     = []

  schedule_expression = var.db_backup_schedule
  cpu                 = var.db_backup_cpu
  memory              = var.db_backup_memory

  environment_variables = {
    DB_BACKUP_BUCKET = module.db_backup_storage.bucket_id
    AWS_REGION       = var.aws_region
  }
  secrets = local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {}

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Embedding-worker fleet. A headless ECS service that drains the broker queue and
# embeds chunks into the staging corpus. It runs under an EXTERNAL deployment
# controller, so a worker-lifecycle lambda must create and scale its task sets -
# that lambda is wired in dev (enable_worker_lifecycle) but not yet in prod, so
# enabling this fleet in prod requires adding the worker-lifecycle module here
# first; without it the service has no task set and runs nothing. Gated off by
# default. Outbound-only (broker, RDS, Voyage) on the shared tasks SG.
module "embed_worker" {
  source = "../modules/worker"
  # Writes embeddings to the database, so it requires RDS as well as its own
  # enable flag.
  count = var.enable_embed_worker && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "embedworker"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/embedworker"]

  cpu           = var.embed_worker_cpu
  memory        = var.embed_worker_memory
  desired_count = var.embed_worker_desired_count

  environment_variables = {
    EMBED_WORKER_CONCURRENCY  = tostring(var.embed_worker_concurrency)
    EMBED_WORKER_MAX_ATTEMPTS = tostring(var.embed_worker_max_attempts)
  }
  secrets = merge(
    {
      EMBEDDING_API_KEY = aws_secretsmanager_secret.embedding_api_key.arn
      RABBITMQ_URL      = module.rabbitmq.url_secret_arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Crawl-worker fleet, the category-crawl counterpart to embed_worker. It drains
# the crawl queue and upserts embedded chunks into the live corpus. Like
# embed_worker it runs under an EXTERNAL deployment controller, so it needs a
# worker-lifecycle lambda to create and scale its task sets - not yet provisioned
# in prod - so enabling it here is foundation-only until that lambda is added.
# Writes to the database, so it requires RDS. Gated off by default.
module "crawl_worker" {
  source = "../modules/worker"
  count  = var.enable_crawl_worker && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "crawlworker"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/crawlworker"]

  cpu           = var.crawl_worker_cpu
  memory        = var.crawl_worker_memory
  desired_count = var.crawl_worker_desired_count

  environment_variables = {
    CRAWL_WORKER_CONCURRENCY  = tostring(var.crawl_worker_concurrency)
    CRAWL_WORKER_MAX_ATTEMPTS = tostring(var.crawl_worker_max_attempts)
  }
  secrets = merge(
    {
      EMBEDDING_API_KEY = aws_secretsmanager_secret.embedding_api_key.arn
      RABBITMQ_URL      = module.rabbitmq.url_secret_arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Category-crawl producer. An on-demand Fargate task (no schedule) that runs the
# wikicrawl binary: it walks the configured Wikipedia categories over the
# MediaWiki Action API, runs the fact-checkability gate, and publishes self-
# contained chunk jobs to the crawl queue (crawl.chunks), then exits. It never
# touches the database (every field a chunk needs travels in the message), so it
# is NOT gated on enable_rds - only on enable_crawl_producer. Launch it with
# `aws ecs run-task` against the family this module outputs, overriding
# CRAWL_CATEGORIES per run. Container name = the bare suffix wikicrawl, so the
# run-ingest-task command override targets it unchanged. Gated off by default.
module "crawl_producer" {
  source = "../modules/scheduled-task"
  count  = var.enable_crawl_producer ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "wikicrawl"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/wikicrawl"]
  # wikicrawl reads its whole configuration from the environment, so it takes no
  # command override; the image's own (empty) command stands.
  command = []

  # No schedule: the producer runs on demand. An empty expression yields a task
  # definition only, launched with `aws ecs run-task`.
  schedule_expression = ""
  cpu                 = var.crawl_producer_cpu
  memory              = var.crawl_producer_memory

  environment_variables = {
    CRAWL_CATEGORIES = var.crawl_categories
    # CRAWL_PROJECT drives both the MediaWiki API host the crawl hits and the
    # corpus tag (which defaults to <project>-crawl), so the two never diverge.
    CRAWL_PROJECT = var.wiki_corpus
  }
  # wikicrawl publishes to the broker and runs the Anthropic-backed gate; it does
  # not embed (the crawlworker fleet does), so it needs only the broker URL and
  # the gate key - least privilege, no embedding key.
  secrets = {
    RABBITMQ_URL        = module.rabbitmq.url_secret_arn
    CHECKWORTHY_API_KEY = aws_secretsmanager_secret.checkworthy_api_key.arn
  }

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Fact-check producer. An on-demand Fargate task (no schedule) that runs the
# factcheckcrawl binary: it reads already-checked French claims from the Google
# Fact Check Tools API and publishes one self-contained curated-claim job per
# reviewed claim to the fact-check queue (factcheck.claims), then exits. It needs
# no database (every political_claims field travels in the message), so it is NOT
# gated on enable_rds - only on enable_factcheck_producer. Launch it with
# `aws ecs run-task`, overriding FACTCHECK_QUERIES per run; the FACTCHECK_API_KEY
# secret is the standing credential. Container name = the bare suffix
# factcheckcrawl. Gated off by default.
module "factcheck_producer" {
  source = "../modules/scheduled-task"
  count  = var.enable_factcheck_producer ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "factcheckcrawl"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/factcheckcrawl"]
  # factcheckcrawl reads its whole configuration from the environment, so it takes
  # no command override; the image's own (empty) command stands.
  command = []

  # No schedule: the producer runs on demand. An empty expression yields a task
  # definition only, launched with `aws ecs run-task`.
  schedule_expression = ""
  cpu                 = var.factcheck_producer_cpu
  memory              = var.factcheck_producer_memory

  environment_variables = {
    FACTCHECK_QUERIES = var.factcheck_queries
  }
  # factcheckcrawl publishes to the broker and reads the aggregator API; it does
  # not touch the database, so it needs only the broker URL and the API key.
  secrets = {
    RABBITMQ_URL      = module.rabbitmq.url_secret_arn
    FACTCHECK_API_KEY = aws_secretsmanager_secret.factcheck_api_key.arn
  }

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Scrutins producer. An on-demand Fargate task (no schedule) that runs the
# scrutinscrawl binary: it conditionally downloads the Assemblee Nationale
# open-data Scrutins archive and publishes one self-contained scrutin job per
# recorded vote to the scrutins queue (scrutins.votes), then exits. It carries no
# secret (the archive is public open data) and needs no database, so it is gated
# only on enable_scrutins_producer. Launch it with `aws ecs run-task`. Container
# name = the bare suffix scrutinscrawl. Gated off by default.
module "scrutins_producer" {
  source = "../modules/scheduled-task"
  count  = var.enable_scrutins_producer ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "scrutinscrawl"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/scrutinscrawl"]
  # scrutinscrawl reads its whole configuration from the environment, so it takes
  # no command override; the image's own (empty) command stands.
  command = []

  # No schedule: the producer runs on demand. An empty expression yields a task
  # definition only, launched with `aws ecs run-task`.
  schedule_expression = ""
  cpu                 = var.scrutins_producer_cpu
  memory              = var.scrutins_producer_memory

  # scrutinscrawl publishes to the broker and reads a public open-data archive; it
  # carries no secret beyond the broker URL.
  secrets = {
    RABBITMQ_URL = module.rabbitmq.url_secret_arn
  }

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Fact-check-worker fleet. A headless ECS service that drains the fact-check queue
# (factcheck.claims), embeds each claim's text through Voyage, and upserts the
# curated claim record into the political claim DB. It mirrors the embedding and
# crawl workers: same EXTERNAL deployment controller (so a worker-lifecycle lambda
# must create and scale its task sets - not yet provisioned in prod, so enabling it
# here is foundation-only until that lambda is added), outbound-only on the shared
# tasks SG. It reuses the CRAWL_WORKER_* tuning the binary reads. Writes to the
# database, so it requires RDS as well as its own enable flag; gated off by default.
module "factcheck_worker" {
  source = "../modules/worker"
  count  = var.enable_factcheck_worker && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "factcheckworker"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/factcheckworker"]

  cpu           = var.factcheck_worker_cpu
  memory        = var.factcheck_worker_memory
  desired_count = var.factcheck_worker_desired_count

  # The fact-check worker reads the shared CRAWL_WORKER_* tuning (the binary calls
  # config.LoadCrawlWorker), so the embedding-fault handling is reused, not
  # redefined.
  environment_variables = {
    CRAWL_WORKER_CONCURRENCY  = tostring(var.factcheck_worker_concurrency)
    CRAWL_WORKER_MAX_ATTEMPTS = tostring(var.factcheck_worker_max_attempts)
  }
  secrets = merge(
    {
      EMBEDDING_API_KEY = aws_secretsmanager_secret.embedding_api_key.arn
      RABBITMQ_URL      = module.rabbitmq.url_secret_arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Scrutins-worker fleet. A headless ECS service that drains the scrutins queue
# (scrutins.votes), parses each scrutin job, and upserts the vote record into the
# database. It mirrors the other worker fleets: same EXTERNAL deployment controller
# (foundation-only in prod until a worker-lifecycle lambda is added),
# outbound-only on the shared tasks SG. The scrutins worker parses and upserts and
# never embeds, so it needs no embedding key (it reads SCRUTINS_WORKER_* tuning).
# Writes to the database, so it requires RDS as well as its own enable flag; gated
# off by default.
module "scrutins_worker" {
  source = "../modules/worker"
  count  = var.enable_scrutins_worker && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "scrutinsworker"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/scrutinsworker"]

  cpu           = var.scrutins_worker_cpu
  memory        = var.scrutins_worker_memory
  desired_count = var.scrutins_worker_desired_count

  environment_variables = {
    SCRUTINS_WORKER_CONCURRENCY  = tostring(var.scrutins_worker_concurrency)
    SCRUTINS_WORKER_MAX_ATTEMPTS = tostring(var.scrutins_worker_max_attempts)
  }
  # The scrutins worker parses and upserts; it does not embed, so it needs no
  # embedding key - least privilege, just the broker URL and the database.
  secrets = merge(
    { RABBITMQ_URL = module.rabbitmq.url_secret_arn },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Observability: centralized log retention is already finite for every service
# (the shared ECS log group in module.ecs, the WAF decision log group in
# module.waf, and every lambda's own group), so this module adds the monitoring
# and alerting half: CloudWatch alarms for ALB 5xx, unhealthy targets, ECS
# running-task drops, RDS and Amazon MQ health, and WAF blocked-request spikes,
# all routed to an SNS topic and a small Slack forwarder Lambda that reads the
# webhook from Secrets Manager (never committed). The CLOUDFRONT-scoped WAF
# publishes its metrics in us-east-1, so the module also takes the us_east_1
# provider for that alarm's same-region SNS + forwarder. Thresholds are all
# variable-driven to keep paging tuned and avoid alert spam.
module "observability" {
  source = "../modules/observability"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  project     = local.project
  environment = var.environment

  slack_webhook_secret_arn = aws_secretsmanager_secret.slack_webhook_url.arn
  log_retention_days       = var.log_retention_days

  alb_arn_suffix = module.alb.arn_suffix
  target_group_arn_suffixes = {
    backend  = module.backend.target_group_arn_suffix
    frontend = module.frontend.target_group_arn_suffix
  }

  cluster_name = module.ecs.cluster_name
  # Only the always-on serving services get a running-task floor alarm. The embed
  # and crawl worker fleets are scale-to-zero by design (the worker-lifecycle
  # controller drops desired count to 0 when their queue is idle), so a min-task
  # floor would false-page an idle-but-healthy worker; they are deliberately
  # excluded from this alarm.
  ecs_service_names = [module.backend.service_name, module.frontend.service_name]

  # one() yields the instance id when RDS is provisioned (count = 1) or null when
  # gated off (count = 0); coalesce maps that null to "" so the module's rds_* alarms
  # disable cleanly without an RDS instance.
  rds_instance_id = coalesce(one(module.rds[*].instance_id), "")

  # The AWS/AmazonMQ Broker dimension value is the broker name, which the rabbitmq
  # module sets to "${project}-${environment}".
  mq_broker_name = "${local.project}-${var.environment}"

  waf_web_acl_name = module.waf.web_acl_name
}

# Network config the deploy workflow needs for `aws ecs run-task`.
resource "aws_ssm_parameter" "private_subnet_ids" {
  name  = "/${local.project}/${var.environment}/deploy/private-subnet-ids"
  type  = "String"
  value = join(",", module.vpc.private_subnet_ids)
}

resource "aws_ssm_parameter" "tasks_security_group_id" {
  name  = "/${local.project}/${var.environment}/deploy/tasks-security-group-id"
  type  = "String"
  value = module.vpc.ecs_tasks_security_group_id
}

# Apply-time IAM permission manifest: the actions the CI apply role must hold to
# provision this environment, surfaced as the apply_required_actions output the
# pre-apply guard (scripts/iam-apply-guard.sh) checks before `terraform apply`.
# include_* track the env's gated areas so the guard never demands a permission
# for a resource the current plan does not create.
module "apply_permissions" {
  source = "../modules/apply-permissions"

  include_acm             = true
  include_cloudfront      = true
  include_waf             = true
  include_rds             = var.enable_rds
  include_scheduled_tasks = var.enable_wiki_sync || var.enable_db_backup
  include_bastion         = var.enable_bastion
  include_observability   = true
  include_elasticache     = var.enable_valkey
}
