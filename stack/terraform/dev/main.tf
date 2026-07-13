data "aws_caller_identity" "current" {}

locals {
  project           = "truth-in-stream"
  github_repository = "verovec/truth-in-stream"

  # The Amazon MQ broker name (aws_mq_broker.broker_name) and the value of the
  # Broker metric dimension. Defined once so the metrics lambda (publisher) and
  # the dashboard (reader) cannot drift to mismatched labels.
  broker_name = "${local.project}-${var.environment}"

  # Effective RDS switch. Dev develops the database locally by default
  # (var.enable_rds = false), but the ingestion hosts need a cloud database to
  # write the corpus into, so enabling them implies RDS - the consumer host would
  # be useless without it. Every "does a managed database exist?" gate below reads
  # local.rds_enabled, never var.enable_rds directly, so the input toggle and the
  # implied one can never disagree (distinct name so the two are not confused).
  rds_enabled = var.enable_rds || var.enable_ingestion_hosts

  # DATABASE_URL secret ARN, or null when RDS is gated off (dev develops the
  # database locally). DB consumers read this: the backend drops the secret and
  # the migration task / embedding worker gate themselves off when it is null.
  rds_dsn_secret_arn = one(module.rds[*].dsn_secret_arn)

  # Exact secret ARNs each ingestion host's instance profile may read - never a
  # wildcard. Split by role so neither host holds a credential it does not use:
  # the crawler runs the producers (broker + the producer-side API keys + the
  # database the dump/stats producers stage in), the consumer runs the workers
  # (broker + database + the embedding key the workers call). compact() drops the
  # DSN if RDS were ever absent, though enable_ingestion_hosts implies it.
  crawler_host_secret_arns = compact([
    module.rabbitmq.url_secret_arn,
    local.rds_dsn_secret_arn,
    aws_secretsmanager_secret.checkworthy_api_key.arn,
    aws_secretsmanager_secret.factcheck_api_key.arn,
  ])
  consumer_host_secret_arns = compact([
    module.rabbitmq.url_secret_arn,
    local.rds_dsn_secret_arn,
    aws_secretsmanager_secret.embedding_api_key.arn,
  ])
  # The tvcapture host runs the TV capture worker, which reaches the backend HTTP
  # API + feed WebSocket (with a Keycloak client-credentials token) and S3 via
  # presigned PUT (the URL carries its own auth). It touches neither the broker nor
  # RDS, so its instance profile reads only its own service-account client secret
  # and the Slack webhook (crash/run alerts) - no broker URL, no DSN, no ECR-side
  # API keys.
  tvcapture_host_secret_arns = [
    aws_secretsmanager_secret.tv_capture_client_secret.arn,
    aws_secretsmanager_secret.slack_webhook_url.arn,
  ]
}

module "vpc" {
  source = "../modules/vpc"

  project     = local.project
  environment = var.environment
  # Single NAT: dev trades AZ-redundant egress for cost.
  nat_gateway_count = 1

  # The ingestion consumer host (and the crawler host's dump/stats producers)
  # write to RDS, so their SGs join the postgres SG's 5432 allow-list when the
  # hosts are provisioned. Added as an inline ingress rule on the postgres SG
  # (an empty list adds no rule). No cycle: the host modules read only the VPC's
  # id/subnets, never the postgres SG - mirrors how prod admits the bastion.
  database_client_security_group_ids = var.enable_ingestion_hosts ? [
    module.crawler_host[0].security_group_id,
    module.consumer_host[0].security_group_id,
  ] : []
}

module "ecs" {
  source = "../modules/ecs"

  project     = local.project
  environment = var.environment
}

module "ecr" {
  source = "../modules/ecr"

  project      = local.project
  environment  = var.environment
  repositories = ["backend", "frontend", "migrate", "backup"]
}

module "rds" {
  source = "../modules/rds"
  count  = local.rds_enabled ? 1 : 0

  project     = local.project
  environment = var.environment

  instance_class      = "db.t4g.micro"
  multi_az            = false
  deletion_protection = false
  skip_final_snapshot = true
  # Dev secrets purge immediately so destroy/apply cycles do not collide with
  # the 7-day recovery window.
  secret_recovery_window_days = 0

  private_subnet_ids = module.vpc.private_subnet_ids
  security_group_id  = module.vpc.postgres_security_group_id
}

# Message broker for the embedding-job queue. Dev runs a single instance in one
# private subnet to keep cost down; nothing consumes the queue yet, so a single
# node is sufficient until the worker fleet drives real throughput.
module "rabbitmq" {
  source = "../modules/rabbitmq"

  project     = local.project
  environment = var.environment

  vpc_id     = module.vpc.vpc_id
  subnet_ids = [module.vpc.private_subnet_ids[0]]
  # The ECS tasks reach the broker in-cluster; the bastion reaches it for the
  # operator's local-worker tunnel, so its SG joins the allow-list only when the
  # bastion is provisioned. Wiring it here (vs. a standalone rule on the broker
  # SG) reuses the broker module's own ingress and avoids the inline-rule
  # conflict a separate aws_*_security_group_rule would cause.
  allowed_security_group_ids = concat(
    [module.vpc.ecs_tasks_security_group_id],
    var.enable_bastion ? [module.bastion[0].security_group_id] : [],
    # The ingestion hosts publish to (crawler) and consume from (consumer) the
    # broker over AMQPS, so both host SGs join the allow-list when provisioned.
    var.enable_ingestion_hosts ? [
      module.crawler_host[0].security_group_id,
      module.consumer_host[0].security_group_id,
    ] : [],
  )

  # The metrics-poller lambda reaches the broker's management API on 443 for
  # per-queue depth, which the application security groups are deliberately not
  # granted. Its SG joins this separate allow-list only when the lambda is
  # provisioned.
  management_allowed_security_group_ids = var.enable_metrics_lambda ? [aws_security_group.metrics_lambda[0].id] : []

  # Dev secrets purge immediately so destroy/apply cycles do not collide with
  # the recovery window.
  secret_recovery_window_days = 0
}

# SSM-only bastion for the develop-locally model: an operator opens a port-forward
# through it to the private broker (scripts/ssm-port-forward.sh) and runs the
# embedding worker locally against the tunnel, draining the cloud queue into the
# local Postgres. No public IP, no SSH, no inbound rules. Gated off by default
# (it is a running instance with a cost); enable it for the duration of an ingest.
module "bastion" {
  source = "../modules/bastion"
  count  = var.enable_bastion ? 1 : 0

  project     = local.project
  environment = var.environment

  vpc_id        = module.vpc.vpc_id
  subnet_id     = module.vpc.private_subnet_ids[0]
  instance_type = var.bastion_instance_type
}

# On-demand EC2 ingestion hosts. Two stop/start-able instances that run the
# existing producer/worker containers directly in the VPC (replacing the
# Fargate-at-zero ingestion path): a crawler host for the producers and a
# consumer host for the worker fleets. Each is SSM-only (no inbound, no SSH key),
# IMDSv2-required, no public IP, with an instance profile scoped to SSM core plus
# only the secrets/ECR-repo/logs its containers use. Their SGs are admitted by the
# broker (5671) and RDS (5432) above/below. Gated off by default (running
# instances cost money); enable for an ingestion run, then stop them to zero cost.
# Enabling them implies RDS (local.rds_enabled), so the consumer has a database to
# write. The operator's start/stop + SSM run command is a separate card.
module "crawler_host" {
  source = "../modules/ingestion-host"
  count  = var.enable_ingestion_hosts ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "crawler-host"

  vpc_id        = module.vpc.vpc_id
  subnet_id     = module.vpc.private_subnet_ids[0]
  instance_type = var.crawler_host_instance_type

  # Producers: broker + the producer-side API keys + the DB the dump/stats
  # producers stage in. No embedding key (workers embed, not producers).
  secret_arns         = local.crawler_host_secret_arns
  ecr_repository_arns = [module.ecr.repository_arns_by_name["backend"]]
}

module "consumer_host" {
  source = "../modules/ingestion-host"
  count  = var.enable_ingestion_hosts ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "consumer-host"

  vpc_id        = module.vpc.vpc_id
  subnet_id     = module.vpc.private_subnet_ids[0]
  instance_type = var.consumer_host_instance_type

  # Workers: broker + database + the embedding key the workers call. No
  # producer-only API keys (checkworthy/factcheck) - least privilege.
  secret_arns         = local.consumer_host_secret_arns
  ecr_repository_arns = [module.ecr.repository_arns_by_name["backend"]]
}

# On-demand TV capture host. A third SSM-only EC2 instance (same shape as the
# crawler/consumer hosts) that runs the long-running tvcapture worker from
# docker-compose.ingest.yml. Unlike those hosts it uses NEITHER the broker nor
# RDS: the worker reaches the backend HTTP API + feed WebSocket
# (TV_CAPTURE_BACKEND_URL) and S3 via presigned PUT, so its SG is deliberately
# NOT admitted to the broker (5671) or postgres (5432) allow-lists above. Its
# instance profile reads only the tv-capture client secret and the Slack webhook.
# Gated off by default (a running instance has a cost); enable it for a capture
# run, then stop it to drop to EBS-only cost. Backend/Keycloak reachability from
# this host is an operator prerequisite (docs/tv-live.md), not wired here.
module "tvcapture_host" {
  source = "../modules/ingestion-host"
  count  = var.enable_ingestion_hosts ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "tvcapture-host"

  vpc_id        = module.vpc.vpc_id
  subnet_id     = module.vpc.private_subnet_ids[0]
  instance_type = var.tvcapture_host_instance_type

  secret_arns         = local.tvcapture_host_secret_arns
  ecr_repository_arns = [module.ecr.repository_arns_by_name["backend"]]
}

# External API keys. Terraform creates the containers only; set the values out
# of band (aws secretsmanager put-secret-value) before the first deploy.
resource "aws_secretsmanager_secret" "embedding_api_key" {
  name                    = "${local.project}/${var.environment}/app/embedding-api-key"
  description             = "Voyage AI API key. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret" "transcription_api_key" {
  name                    = "${local.project}/${var.environment}/app/transcription-api-key"
  description             = "AssemblyAI API key. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

# Anthropic API key for the crawl producer's fact-checkability gate
# (CHECKWORTHY_API_KEY). Created empty; the value is set out of band. Consumed by
# the wikicrawl producer to judge whether each crawled chunk is citable evidence
# before it is published.
resource "aws_secretsmanager_secret" "checkworthy_api_key" {
  name                    = "${local.project}/${var.environment}/app/checkworthy-api-key"
  description             = "Anthropic API key for the crawl fact-checkability gate. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

# Google Fact Check Tools API key for the fact-check producer (FACTCHECK_API_KEY).
# Created empty; the value is set out of band. Consumed by the factcheckcrawl
# producer to read already-checked French claims from the aggregator API.
resource "aws_secretsmanager_secret" "factcheck_api_key" {
  name                    = "${local.project}/${var.environment}/app/factcheck-api-key"
  description             = "Google Fact Check Tools API key for the fact-check crawl producer. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

# Keycloak service-account client secret for the tvcapture worker
# (TV_CAPTURE_CLIENT_SECRET). Created empty; the value is set out of band. The
# worker authenticates to the backend as the scoped `tv-capture` client via the
# client-credentials grant - it never holds an admin credential.
resource "aws_secretsmanager_secret" "tv_capture_client_secret" {
  name                    = "${local.project}/${var.environment}/app/tv-capture-client-secret"
  description             = "Keycloak tv-capture client-credentials secret for the TV capture worker. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

# Slack incoming webhook for TV capture run/crash alerts (SLACK_WEBHOOK_URL).
# Created empty; the value is set out of band. Mirrors the prod slack_webhook_url
# container. Optional at runtime; alerts are skipped when unset.
resource "aws_secretsmanager_secret" "slack_webhook_url" {
  name                    = "${local.project}/${var.environment}/app/slack-webhook-url"
  description             = "Slack incoming webhook URL. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

module "media_storage" {
  source = "../modules/s3"

  project     = local.project
  environment = var.environment

  cors_allowed_origins = var.media_cors_allowed_origins
  # Backstop lifecycle prune of the TV recordings/ prefix. Default 0 (off): the
  # app-level daily retention job is authoritative. Set > 0 only as a safety net.
  recordings_retention_days = var.recordings_retention_days
}

# Durable home for local pg_dump snapshots, so the embedded corpus survives a
# reset or a new machine without re-embedding. Backups are taken and restored
# with operator credentials via `make backup` / `make restore`; no automated
# job consumes the bucket yet, so no dedicated IAM principal is provisioned.
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

  ecr_repository_arns  = module.ecr.repository_arns
  cluster_arn          = module.ecs.cluster_id
  media_bucket_arn     = module.media_storage.bucket_arn
  db_backup_bucket_arn = module.db_backup_storage.bucket_arn
  secret_arns = concat(
    local.rds_dsn_secret_arn != null ? [local.rds_dsn_secret_arn] : [],
    [
      aws_secretsmanager_secret.embedding_api_key.arn,
      aws_secretsmanager_secret.transcription_api_key.arn,
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

module "alb" {
  source = "../modules/alb"

  project     = local.project
  environment = var.environment

  public_subnet_ids = module.vpc.public_subnet_ids
  security_group_id = module.vpc.alb_security_group_id
  # No domain yet: plain HTTP on the ALB DNS name. Set certificate_arn once a
  # hosted zone + ACM certificate exist to switch to HTTPS with redirect.
}

module "backend" {
  source = "../modules/service"

  project     = local.project
  environment = var.environment
  name        = "backend"

  image          = "${module.ecr.repository_urls["backend"]}:latest"
  container_port = 8080

  environment_variables = {
    PORT = "8080"
    # Object storage uses the task role for credentials (no endpoint, no static
    # keys); only the bucket and region are configured here.
    STORAGE_BUCKET = module.media_storage.bucket_id
    STORAGE_REGION = var.aws_region
  }
  secrets = merge(
    {
      EMBEDDING_API_KEY     = aws_secretsmanager_secret.embedding_api_key.arn
      TRANSCRIPTION_API_KEY = aws_secretsmanager_secret.transcription_api_key.arn
    },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_id              = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_id       = module.vpc.ecs_tasks_security_group_id
  alb_security_group_id   = module.vpc.alb_security_group_id
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

  environment_variables = {
    PORT                    = "3000"
    NEXT_TELEMETRY_DISABLED = "1"
  }

  cluster_id              = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_id       = module.vpc.ecs_tasks_security_group_id
  alb_security_group_id   = module.vpc.alb_security_group_id
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
# does. With the database developed locally (enable_rds = false) there is nothing
# to migrate in the cloud, and the deploy workflow's migrate step is skipped.
module "migration" {
  source = "../modules/migration"
  count  = local.rds_enabled ? 1 : 0

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
  count = var.enable_wiki_sync && local.rds_enabled ? 1 : 0

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
  count = var.enable_db_backup && local.rds_enabled ? 1 : 0

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

# Queue observability. A scheduled lambda polls the broker's RabbitMQ management
# API (Amazon MQ exposes no per-queue metrics natively) and republishes per-queue
# backlog, publish rate and consumer count as custom CloudWatch metrics. The
# dashboard charts them and the `/consumer status` action reads the same Backlog
# metric per queue. Gated off by default; enable it alongside the broker to watch
# the queues the EC2 ingestion hosts fill and drain. The lambda binary is built by
# `make lambda-mqmetrics` in stack/backend before apply.

# The lambda's security group lives here (not in the module) so it can be granted
# on the broker's management allow-list without a module dependency cycle, the
# same way the application tasks SG is shared from the vpc module. Egress-only:
# it reaches the in-VPC broker and the AWS control plane (Secrets Manager,
# CloudWatch) via the NAT gateway.
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

module "metrics_lambda" {
  source = "../modules/metrics-lambda"
  count  = var.enable_metrics_lambda ? 1 : 0

  project     = local.project
  environment = var.environment

  source_binary_path = "${path.module}/../../backend/build/mqmetrics/bootstrap"

  subnet_ids         = module.vpc.private_subnet_ids
  security_group_ids = [aws_security_group.metrics_lambda[0].id]

  rabbitmq_url_secret_arn = module.rabbitmq.url_secret_arn
  broker_name             = local.broker_name
  metrics_namespace       = var.metrics_namespace

  schedule_expression = var.metrics_poll_schedule
}

# The dashboard visualises the lambda's queue metrics, so it is bound to the same
# enable flag. The ingestion workers now run on the EC2 consumer host, not an ECS
# service, so no worker service name is passed and the worker widgets are omitted;
# the queue-backlog charts remain.
module "monitoring" {
  source = "../modules/monitoring"
  count  = var.enable_metrics_lambda ? 1 : 0

  project     = local.project
  environment = var.environment

  metrics_namespace = var.metrics_namespace
  broker_name       = local.broker_name
  queue_base        = "embedding.jobs"

  cluster_name = module.ecs.cluster_name
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

  include_rds             = local.rds_enabled
  include_scheduled_tasks = var.enable_wiki_sync || var.enable_db_backup
  include_bastion         = var.enable_bastion
  include_ingestion_hosts = var.enable_ingestion_hosts
  include_metrics_lambda  = var.enable_metrics_lambda
}
