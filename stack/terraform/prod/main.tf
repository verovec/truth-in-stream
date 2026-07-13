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

  # Self-hosted Keycloak requires RDS (its realm/user store is a dedicated
  # database on the shared instance), so it is on only when both flags are set.
  keycloak_enabled = var.enable_keycloak && var.enable_rds

  # Public issuer host and the JDBC URL to the scoped keycloak database on the
  # shared RDS instance (host:port from RDS; the database itself is created by
  # the keycloak DB-bootstrap task). The URL carries no credentials, so it rides
  # on plain environment variables; the username is a literal and the password is
  # a Secrets Manager secret. Guarded so the null RDS endpoint is never
  # interpolated when the stack is gated off.
  keycloak_issuer_host = "login.${var.domain_name}"
  keycloak_db_url      = local.keycloak_enabled ? "jdbc:postgresql://${one(module.rds[*].endpoint)}/keycloak?sslmode=require" : ""

  # Public OIDC issuer the app (backend RequireIdentity gate + frontend OIDC flow)
  # validates against. Unconditional: it points at login.<domain> whether Keycloak
  # is self-hosted here or run out of band. Prod uses the single public issuer for
  # both the browser and the back-channel (the tasks reach it via CloudFront), so
  # no JWKS/internal-host override is set - matching the dev-networking spec's
  # stated production behaviour.
  keycloak_issuer_url = "https://${local.keycloak_issuer_host}/realms/truth-in-stream"

  # OIDC client id, the authorized party (azp) the backend validates and the
  # frontend authenticates as. Single source of truth: set explicitly on BOTH
  # services (rather than letting the backend fall back to its compiled-in
  # default), so a client rename changes one place and can never leave the two
  # sides in disagreement (which would reject every token and 401 all of /api).
  keycloak_client_id = "truth-in-stream-web"
}

# Public TLS certificate for jeminforme.fr (apex + www + login), requested in
# us-east-1 for CloudFront. login.<domain> fronts the self-hosted Keycloak.
# The login SAN (and the matching CloudFront alias + DNS records below) are
# deliberately unconditional, NOT gated on enable_keycloak: keeping a stable SAN
# set avoids a full certificate replacement (which re-validates every domain on
# the cert) whenever the flag is flipped, and login is self-hosted by default.
# If enable_keycloak=false to run Keycloak out of band, the operator owns
# login.<domain> DNS and repoints it; the unused SAN/alias are harmless.
# DNS validation records are NOT created here: the authoritative
# hosted zone is in the main account (<main-account-id>), so the main-account
# terraform root (VER-140) creates the records by reading this module's
# domain_validation_options output. The certificate is PENDING_VALIDATION until
# those records exist; that does not block this plan.
module "acm" {
  source = "../modules/acm"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  domain_name               = var.domain_name
  subject_alternative_names = ["www.${var.domain_name}", "login.${var.domain_name}"]
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

  project     = local.project
  environment = var.environment
  # The keycloak repo is created only when the self-hosted identity provider is
  # actually provisioned (enable_keycloak AND enable_rds, i.e. keycloak_enabled),
  # matching the module/secret gating so no combination leaves an orphan repo.
  repositories = concat(
    ["backend", "frontend", "migrate", "backup"],
    local.keycloak_enabled ? ["keycloak"] : [],
  )
}

# pgvector-tuned parameter group for the prod vector store. Holds the memory and
# parallelism knobs a hundreds-of-GB HNSW workload needs, expressed as RDS
# formulas so they track the instance memory (DBInstanceClassMemory is in bytes;
# shared_buffers/effective_cache_size are in 8 KB pages, work_mem/
# maintenance_work_mem in KB) and stay correct if rds_instance_class is resized.
# family = postgres17 matches the RDS engine major (modules/rds pins
# engine_version = "17"); the current default 17.x minor ships pgvector 0.8.2
# (>= 0.8.0, so iterative scans are available), and auto_minor_version_upgrade
# only moves forward.
#
# The pgvector search GUCs hnsw.ef_search and hnsw.iterative_scan are
# DELIBERATELY NOT set here: on RDS they are not part of the postgres17 parameter
# family and the master role (rds_superuser, not a true superuser) is denied
# setting them via the parameter group or ALTER ROLE/DATABASE, so listing them
# would fail the apply. They stay session-scoped, set per connection in the Go
# layer (stack/backend/internal/store/postgres/postgres.go sets hnsw.ef_search
# via set_config on AfterConnect); iterative_scan is left at its default off and
# opted into per query where a filter under-returns.
resource "aws_db_parameter_group" "vector" {
  count = var.enable_rds ? 1 : 0

  name        = "${local.project}-${var.environment}-pgvector"
  family      = "postgres17"
  description = "pgvector-tuned parameters for the prod vector store (VER-177)."

  # 25% of instance memory (the RDS default), set explicitly to document the
  # sizing decision. Static, so it applies on the next reboot, not live.
  parameter {
    name         = "shared_buffers"
    value        = "{DBInstanceClassMemory/32768}"
    apply_method = "pending-reboot"
  }

  # 75% of instance memory: a planner hint (no allocation) so the planner assumes
  # most of the index/data is served from cache.
  parameter {
    name  = "effective_cache_size"
    value = "{DBInstanceClassMemory*3/32768}"
  }

  # 16 MiB (up from the 4 MiB default) for the sort/limit work behind a top-k
  # vector query. Kept modest because work_mem is per node and multiplies across
  # concurrent queries; raise it only for the heavy query path via
  # ALTER ROLE ... SET work_mem rather than globally.
  parameter {
    name  = "work_mem"
    value = "16384"
  }

  # ~6.25% of instance memory for HNSW index builds (the in-progress graph wants
  # to fit here or the build slows sharply). Shared across parallel build workers.
  parameter {
    name  = "maintenance_work_mem"
    value = "{DBInstanceClassMemory/16384}"
  }

  # Parallel HNSW index builds (pgvector >= 0.6.0). 4 leaves headroom under the
  # r7g.2xlarge's 8 vCPU and the default max_parallel_workers (8); raise it with
  # the instance size.
  parameter {
    name  = "max_parallel_maintenance_workers"
    value = "4"
  }
}

module "rds" {
  source = "../modules/rds"
  count  = var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment

  # Memory-optimized r-family sized for the vector store (see rds_instance_class).
  # Backups (21-day retention), deletion protection, and the final snapshot are
  # independent of Multi-AZ and stay on; only the standby replica is dropped. Set
  # rds_multi_az = true to restore failover HA.
  instance_class        = var.rds_instance_class
  multi_az              = var.rds_multi_az
  deletion_protection   = true
  skip_final_snapshot   = false
  backup_retention_days = 21

  # Hundreds-of-GB gp3 volume with IOPS/throughput provisioned above the gp3
  # baseline (valid because allocated_storage >= the 400 GiB gp3 threshold).
  allocated_storage     = var.rds_allocated_storage
  max_allocated_storage = var.rds_max_allocated_storage
  iops                  = var.rds_iops
  storage_throughput    = var.rds_storage_throughput

  # The pgvector-tuned parameter group above. Attaching it puts the static
  # shared_buffers change in pending-reboot; the operator reboots to apply it.
  parameter_group_name = aws_db_parameter_group.vector[0].name

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

  # The metrics-poller lambda reaches the broker's management API on 443 for
  # per-queue depth; its SG joins the separate management allow-list only when the
  # lambda is provisioned.
  management_allowed_security_group_ids = var.enable_metrics_lambda ? [aws_security_group.metrics_lambda[0].id] : []

  # Weekly maintenance reboot off the daily producer cron slots (defaults in the
  # module: SUNDAY 07:00 UTC).
  maintenance_window_day  = var.mq_maintenance_window_day
  maintenance_window_time = var.mq_maintenance_window_time
}

# The metrics-poller lambda's security group lives here (not in the module) so it
# can be granted on the broker's management allow-list without a module dependency
# cycle. Egress-only: it reaches the in-VPC broker and the AWS control plane
# (Secrets Manager, CloudWatch) via the NAT gateways.
resource "aws_security_group" "metrics_lambda" {
  count = var.enable_metrics_lambda ? 1 : 0

  name        = "${local.project}-${var.environment}-mqmetrics"
  description = "Metrics-poller lambda: egress only (RabbitMQ management API in-VPC, AWS APIs via NAT)."
  vpc_id      = module.vpc.vpc_id

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${local.project}-${var.environment}-mqmetrics" }
}

# Metrics-poller lambda + ingestion queue dashboard, on by default so prod queue
# telemetry (and the alarms that key on it) exist in a fresh plan. The lambda
# binary is built by `make lambda-mqmetrics` in stack/backend before apply.
module "metrics_lambda" {
  source = "../modules/metrics-lambda"
  count  = var.enable_metrics_lambda ? 1 : 0

  project     = local.project
  environment = var.environment

  source_binary_path = "${path.module}/../../backend/build/mqmetrics/bootstrap"

  subnet_ids         = module.vpc.private_subnet_ids
  security_group_ids = [aws_security_group.metrics_lambda[0].id]

  rabbitmq_url_secret_arn = module.rabbitmq.url_secret_arn
  broker_name             = "${local.project}-${var.environment}"
  metrics_namespace       = var.metrics_namespace
  queue_names             = var.metrics_queue_bases

  schedule_expression = var.metrics_poll_schedule
}

module "monitoring" {
  source = "../modules/monitoring"
  count  = var.enable_metrics_lambda ? 1 : 0

  project     = local.project
  environment = var.environment

  metrics_namespace = var.metrics_namespace
  broker_name       = "${local.project}-${var.environment}"
  queue_base        = var.metrics_queue_bases[0]

  cluster_name = module.ecs.cluster_name
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

# Legacy operator password login (retired). The /api gate uses the verified
# Keycloak identity; the backend reads these only when LEGACY_PASSWORD_LOGIN is
# on, so the containers exist only when enable_legacy_password_login re-enables
# that path. Values set out of band, never in Terraform.
resource "aws_secretsmanager_secret" "auth_email" {
  count       = var.enable_legacy_password_login ? 1 : 0
  name        = "${local.project}/${var.environment}/app/auth-email"
  description = "Operator login email. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "auth_password_hash" {
  count       = var.enable_legacy_password_login ? 1 : 0
  name        = "${local.project}/${var.environment}/app/auth-password-hash"
  description = "Operator argon2id password hash. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "session_secret" {
  count       = var.enable_legacy_password_login ? 1 : 0
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

# Keycloak service-account client secret for the tvcapture worker
# (TV_CAPTURE_CLIENT_SECRET). Container only, value set out of band. Prod does not
# run the TV capture worker yet: like the embed/crawl worker fleets above, the
# prod tvcapture runtime convergence onto the dev ingestion-host model is future
# work (dev runs it on an on-demand EC2 host - see stack/terraform/dev/main.tf and
# docs/tv-live.md). Declared here so the secret container and the push-secrets
# allowlist stay consistent across environments.
resource "aws_secretsmanager_secret" "tv_capture_client_secret" {
  name        = "${local.project}/${var.environment}/app/tv-capture-client-secret"
  description = "Keycloak tv-capture client-credentials secret for the TV capture worker. Value set manually, never in Terraform."
}

# Keycloak master-realm bootstrap admin password (first-boot admin console
# access). Generated by Terraform and written into the secret below, mirroring
# the keycloak DB password pattern, so first-boot admin login is never blocked
# on a manual push. Created only when Keycloak is self-hosted here.
resource "aws_secretsmanager_secret" "keycloak_bootstrap_admin_password" {
  count       = local.keycloak_enabled ? 1 : 0
  name        = "${local.project}/${var.environment}/keycloak/bootstrap-admin-password"
  description = "Keycloak bootstrap admin password. Generated by Terraform, never committed."
}

# Generated password value for the bootstrap admin. No special characters so it
# stays safe in the container env, the admin console, and CLI use, matching the
# keycloak DB password pattern. Keycloak reads it as KC_BOOTSTRAP_ADMIN_PASSWORD.
resource "random_password" "keycloak_bootstrap_admin" {
  count   = local.keycloak_enabled ? 1 : 0
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret_version" "keycloak_bootstrap_admin_password" {
  count         = local.keycloak_enabled ? 1 : 0
  secret_id     = aws_secretsmanager_secret.keycloak_bootstrap_admin_password[0].id
  secret_string = random_password.keycloak_bootstrap_admin[0].result
}

# Password for the scoped `keycloak` Postgres role. Generated by Terraform (no
# special characters so it stays URL/DSN-safe, matching the RDS master pattern)
# and written into the secret. The bootstrap task (below) reads it to create the
# role; the Keycloak service reads it as KC_DB_PASSWORD. Single source of truth.
resource "random_password" "keycloak_db" {
  count   = local.keycloak_enabled ? 1 : 0
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "keycloak_db_password" {
  count       = local.keycloak_enabled ? 1 : 0
  name        = "${local.project}/${var.environment}/keycloak/db-password"
  description = "Password for the scoped keycloak Postgres role. Generated by Terraform, never committed."
}

resource "aws_secretsmanager_secret_version" "keycloak_db_password" {
  count         = local.keycloak_enabled ? 1 : 0
  secret_id     = aws_secretsmanager_secret.keycloak_db_password[0].id
  secret_string = random_password.keycloak_db[0].result
}

module "media_storage" {
  source = "../modules/s3"

  project     = local.project
  environment = var.environment

  # Browser origins allowed to PUT/GET media via presigned URLs. Defaults to the
  # app origin(s) now that a fixed domain exists (no longer "*"); an explicit
  # media_cors_allowed_origins overrides.
  cors_allowed_origins = length(var.media_cors_allowed_origins) > 0 ? var.media_cors_allowed_origins : ["https://${var.domain_name}", "https://www.${var.domain_name}"]
  # Backstop lifecycle prune of the TV recordings/ prefix. Default 0 (off): the
  # app-level daily retention job is authoritative. Set > 0 only as a safety net.
  recordings_retention_days = var.recordings_retention_days
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
  # Producers publish a per-source RunSuccess metric; the task role gets a
  # namespace-scoped PutMetricData grant only while this is set.
  run_metrics_namespace = var.run_metrics_namespace
  secret_arns = concat(
    local.rds_dsn_secret_arn != null ? [local.rds_dsn_secret_arn] : [],
    [
      aws_secretsmanager_secret.embedding_api_key.arn,
      aws_secretsmanager_secret.transcription_api_key.arn,
      aws_secretsmanager_secret.deepseek_api_key.arn,
      aws_secretsmanager_secret.gemini_api_key.arn,
      aws_secretsmanager_secret.slack_webhook_url.arn,
      aws_secretsmanager_secret.checkworthy_api_key.arn,
      aws_secretsmanager_secret.factcheck_api_key.arn,
      module.rabbitmq.url_secret_arn,
    ],
    # Splat yields [] when the resource is gated off (count = 0), [arn] when on.
    # Legacy auth secrets exist only under enable_legacy_password_login; the
    # keycloak secrets only under keycloak_enabled.
    aws_secretsmanager_secret.auth_email[*].arn,
    aws_secretsmanager_secret.auth_password_hash[*].arn,
    aws_secretsmanager_secret.session_secret[*].arn,
    aws_secretsmanager_secret.keycloak_bootstrap_admin_password[*].arn,
    aws_secretsmanager_secret.keycloak_db_password[*].arn,
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
# and never cached. It also serves login.<domain> as an alias: the AllViewer
# origin-request policy forwards the viewer Host, so login. requests reach the
# internal ALB with their original host and hit the Keycloak host-header rule (no
# extra cache behavior needed, since every behavior targets the same VPC origin).
# Media is served from direct presigned S3 URLs and is not fronted here. The
# WAFv2 web ACL above is associated via web_acl_id; the apex/www/login alias
# records are created by the main-account root (VER-140), which reads domain_name
# and hosted_zone_id.
module "cloudfront" {
  source = "../modules/cloudfront"

  project     = local.project
  environment = var.environment

  alb_arn         = module.alb.arn
  alb_dns_name    = module.alb.dns_name
  certificate_arn = module.acm.certificate_arn
  aliases         = [var.domain_name, "www.${var.domain_name}", "login.${var.domain_name}"]
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
      # /api is gated on the verified Keycloak identity (RequireIdentity). The
      # backend derives the JWKS URL from this issuer; prod uses the single public
      # issuer, so no KEYCLOAK_JWKS_URL override is set. The client id is set
      # explicitly (not left to the compiled-in default) so it shares one source
      # with the frontend.
      KEYCLOAK_ISSUER    = local.keycloak_issuer_url
      KEYCLOAK_CLIENT_ID = local.keycloak_client_id
    },
    # REDIS_URL turns on the 24h analysis cache. No auth token, so it is a plain
    # env var, not a secret; absent when the Valkey cache is gated off.
    local.redis_url != null ? { REDIS_URL = local.redis_url } : {},
    # Re-enable the retired operator password login only when explicitly asked.
    var.enable_legacy_password_login ? { LEGACY_PASSWORD_LOGIN = "true" } : {},
  )
  secrets = merge(
    {
      EMBEDDING_API_KEY     = aws_secretsmanager_secret.embedding_api_key.arn
      TRANSCRIPTION_API_KEY = aws_secretsmanager_secret.transcription_api_key.arn
      DEEPSEEK_API_KEY      = aws_secretsmanager_secret.deepseek_api_key.arn
      GEMINI_API_KEY        = aws_secretsmanager_secret.gemini_api_key.arn
      SLACK_WEBHOOK_URL     = aws_secretsmanager_secret.slack_webhook_url.arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
    # Legacy password-login secrets are injected only when that path is enabled;
    # RequireIdentity (Keycloak) is the default gate and reads none of these.
    var.enable_legacy_password_login ? {
      AUTH_EMAIL         = aws_secretsmanager_secret.auth_email[0].arn
      AUTH_PASSWORD_HASH = aws_secretsmanager_secret.auth_password_hash[0].arn
      SESSION_SECRET     = aws_secretsmanager_secret.session_secret[0].arn
    } : {},
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
    # OIDC login flow. Issuer and client id are public identifiers (the public
    # PKCE client carries no secret). NEXT_PUBLIC_APP_URL sets the redirect /
    # post-logout URIs, which must match the realm client's registered origins.
    # config.ts (server-only) reads NEXT_PUBLIC_APP_URL at runtime via its
    # injectable env default - it is not accessed as a build-time-inlined
    # `process.env.NEXT_PUBLIC_*` literal, so a runtime task env var suffices and
    # no image build-arg is needed.
    KEYCLOAK_ISSUER     = local.keycloak_issuer_url
    KEYCLOAK_CLIENT_ID  = local.keycloak_client_id
    NEXT_PUBLIC_APP_URL = "https://${var.domain_name}"
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

# Self-hosted Keycloak: the production identity provider, an ECS Fargate service
# behind the internal ALB, reached from the browser at https://login.<domain>
# through CloudFront (the login cert SAN + alias are added by the edge card,
# VER-158). It runs in Keycloak production mode with edge TLS termination:
# KC_HOSTNAME is the public https URL, KC_PROXY_HEADERS=xforwarded trusts the
# CloudFront/ALB forwarded headers, and KC_HTTP_ENABLED serves plain HTTP on the
# private hop. Its realm/user store is a dedicated `keycloak` database on the
# shared RDS instance (created by the DB-bootstrap task, VER-157); the scoped
# role password is a secret, the JDBC URL is not. Health checks hit /health/ready
# on the management port. Gated by enable_keycloak (requires RDS).
module "keycloak" {
  source = "../modules/keycloak"
  count  = local.keycloak_enabled ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "keycloak"

  image         = "${module.ecr.repository_urls["keycloak"]}:latest"
  cpu           = var.keycloak_cpu
  memory        = var.keycloak_memory
  desired_count = var.keycloak_desired_count

  # Distinct traffic port on the shared ECS-tasks security group: the backend
  # already owns an 8080-from-ALB ingress rule on this SG, so Keycloak uses 8081
  # to avoid a duplicate rule (each service on the shared SG uses its own port).
  container_port = 8081

  # Only RUNTIME options here. Build-time options (KC_DB, health, metrics) are
  # baked into the --optimized image by `kc.sh build`; re-setting them at runtime
  # is redundant and a mismatch would make --optimized refuse to start.
  environment_variables = {
    KC_DB_URL                   = local.keycloak_db_url
    KC_DB_USERNAME              = "keycloak"
    KC_HTTP_PORT                = "8081"
    KC_HOSTNAME                 = "https://${local.keycloak_issuer_host}"
    KC_HOSTNAME_STRICT          = "true"
    KC_PROXY_HEADERS            = "xforwarded"
    KC_HTTP_ENABLED             = "true"
    KC_BOOTSTRAP_ADMIN_USERNAME = var.keycloak_admin_username
  }
  secrets = {
    KC_DB_PASSWORD              = aws_secretsmanager_secret.keycloak_db_password[0].arn
    KC_BOOTSTRAP_ADMIN_PASSWORD = aws_secretsmanager_secret.keycloak_bootstrap_admin_password[0].arn
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
  listener_rule_priority = 5
  host_headers           = [local.keycloak_issuer_host]
  health_check_path      = "/health/ready"
}

# One-shot task that creates the dedicated `keycloak` database and its scoped
# role on the shared RDS instance (Keycloak makes its own tables but not its
# database). The keycloak deploy runs it with `aws ecs run-task` before rolling
# the service (see _deploy.yml). It connects as the RDS master (DATABASE_URL) and
# reads the generated role password (KC_DB_PASSWORD) - both ARNs are already
# readable by the shared task-execution role (wired above), so no extra IAM is
# needed. The SQL is idempotent (create role/db only if absent, always re-sync
# the password), so it runs safely on every deploy and a re-run is a no-op.
#
# Reuses the generic scheduled-task module with an empty schedule_expression,
# which emits a task definition only (no EventBridge schedule) for on-demand
# run-task - the same one-shot shape the ingestion producers use. The password is
# read into psql with \getenv (never on the command line) and interpolated with
# the :'kcpass' quoting form, so it is neither logged nor injectable.
module "keycloak_db_bootstrap" {
  source = "../modules/scheduled-task"
  count  = local.keycloak_enabled ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "keycloak-db-bootstrap"

  image               = var.keycloak_db_bootstrap_image
  schedule_expression = ""
  entry_point         = ["/bin/sh", "-c"]
  command = [
    <<-EOT
    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
    \getenv kcpass KC_DB_PASSWORD
    SELECT 'CREATE ROLE keycloak LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'keycloak')\gexec
    ALTER ROLE keycloak WITH LOGIN PASSWORD :'kcpass';
    SELECT 'CREATE DATABASE keycloak OWNER keycloak' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'keycloak')\gexec
    SQL
    EOT
  ]

  cpu    = 256
  memory = 512

  secrets = {
    DATABASE_URL   = local.rds_dsn_secret_arn
    KC_DB_PASSWORD = aws_secretsmanager_secret.keycloak_db_password[0].arn
  }

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
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
# controller, so an external controller must create and scale its task sets;
# prod provisions none, so without one the service has no task set and runs
# nothing. Dormant foundation, gated off by default: dev's on-demand ingestion
# moved to EC2 hosts (docs/ingestion-hosts.md), so a future card should converge
# prod onto that model or remove this fleet. Outbound-only (broker, RDS, Voyage)
# on the shared tasks SG.
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
# embed_worker it runs under an EXTERNAL deployment controller, so it needs an
# external controller to create and scale its task sets - none is provisioned
# in prod - so enabling it here is foundation-only until one is added.
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
# CRAWL_CATEGORIES per run. Container name = the bare suffix wikicrawl, so an
# `aws ecs run-task --overrides` targets it unchanged. Gated off by default.
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
    # Publish a per-source RunSuccess metric so the no-successful-run alarm sees
    # this producer; empty disables it (the task role's metric grant is likewise
    # gated on the namespace).
    RUN_METRICS_NAMESPACE = var.run_metrics_namespace
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
    FACTCHECK_QUERIES     = var.factcheck_queries
    RUN_METRICS_NAMESPACE = var.run_metrics_namespace
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

  environment_variables = {
    RUN_METRICS_NAMESPACE = var.run_metrics_namespace
  }
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
# crawl workers: same EXTERNAL deployment controller (which must create and scale
# its task sets - none is provisioned in prod, so enabling it here is
# foundation-only until one is added), outbound-only on the shared
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
# (foundation-only in prod until such a controller is added),
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
  # and crawl worker fleets are scale-to-zero foundation (an external controller
  # would drop desired count to 0 when their queue is idle), so a min-task
  # floor would false-page an idle-but-healthy worker; they are deliberately
  # excluded from this alarm.
  ecs_service_names = [module.backend.service_name, module.frontend.service_name]

  # one() yields the instance id when RDS is provisioned (count = 1) or null when
  # gated off (count = 0); coalesce maps that null to "" so the module's rds_* alarms
  # disable cleanly without an RDS instance.
  rds_instance_id = coalesce(one(module.rds[*].instance_id), "")

  # The AWS/AmazonMQ Broker dimension value is the broker name, which the rabbitmq
  # module sets to "${project}-${environment}". It is also the Broker dimension the
  # metrics lambda stamps on every queue datum, so the queue alarms reuse it.
  mq_broker_name = "${local.project}-${var.environment}"

  waf_web_acl_name = module.waf.web_acl_name

  # Ingestion queue + run alarms. The queue alarms key on the metrics lambda's
  # custom metrics (enabled with it); the run alarms key on the producers'
  # RunSuccess metric. Each set disables cleanly when its namespace is empty.
  queue_metrics_namespace = var.enable_metrics_lambda ? var.metrics_namespace : ""
  queue_bases             = var.metrics_queue_bases
  run_metrics_namespace   = var.run_metrics_namespace
  run_sources             = var.run_sources
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
  include_metrics_lambda  = var.enable_metrics_lambda
}
