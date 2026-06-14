data "aws_caller_identity" "current" {}

locals {
  project           = "truth-in-stream"
  github_repository = "verovec/truth-in-stream"

  # The Amazon MQ broker name (aws_mq_broker.broker_name) and the value of the
  # Broker metric dimension. Defined once so the metrics lambda (publisher) and
  # the dashboard (reader) cannot drift to mismatched labels.
  broker_name = "${local.project}-${var.environment}"

  # DATABASE_URL secret ARN, or null when RDS is gated off (dev develops the
  # database locally). DB consumers read this: the backend drops the secret and
  # the migration task / embedding worker gate themselves off when it is null.
  rds_dsn_secret_arn = one(module.rds[*].dsn_secret_arn)
}

module "vpc" {
  source = "../modules/vpc"

  project     = local.project
  environment = var.environment
  # Single NAT: dev trades AZ-redundant egress for cost.
  nat_gateway_count = 1
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
  count  = var.enable_rds ? 1 : 0

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
  )

  # The metrics-poller and worker-lifecycle lambdas reach the broker's management
  # API on 443 for per-queue depth, which the application security groups are
  # deliberately not granted. Their SGs join this separate allow-list only when the
  # respective lambda is provisioned.
  management_allowed_security_group_ids = concat(
    var.enable_metrics_lambda ? [aws_security_group.metrics_lambda[0].id] : [],
    var.enable_worker_lifecycle ? [aws_security_group.worker_lifecycle[0].id] : [],
  )

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

module "media_storage" {
  source = "../modules/s3"

  project     = local.project
  environment = var.environment

  cors_allowed_origins = var.media_cors_allowed_origins
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
# embeds chunks into the staging corpus; scale embed_worker_desired_count to scale
# throughput. Outbound-only (broker, RDS, Voyage) on the shared tasks SG. Gated
# off by default; enable during a corpus ingest that publishes jobs.
module "embed_worker" {
  source = "../modules/worker"
  # Writes embeddings to the database, so it requires RDS as well as its own
  # enable flag. The cloud worker stays gated off while the database is local.
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

# Embedding-queue producer. An on-demand Fargate task (no schedule) that runs the
# wikisync producer in publish-only mode: it ingests the corpus dump and fills the
# broker queue with self-contained, versioned embedding jobs, then exits - the
# consumer fleet owns the drain and the live swap. Launch it with
# `aws ecs run-task` against the task definition this module outputs. Gated off by
# default; it needs a database to stage and read chunks (RDS, or a tunnelled local
# database), so it is bound to enable_rds like the other DB-backed tasks.
module "producer" {
  source = "../modules/scheduled-task"
  count  = var.enable_producer && var.enable_rds ? 1 : 0

  project     = local.project
  environment = var.environment
  name        = "producer"

  image       = "${module.ecr.repository_urls["backend"]}:latest"
  entry_point = ["/wikisync"]
  command     = ["-mode=bulk", "-publish-only"]

  # No schedule: the producer runs on demand, not on a cron - re-ingesting a full
  # corpus on a timer would be wasteful. An empty expression yields a task
  # definition only.
  schedule_expression = ""
  cpu                 = var.producer_cpu
  memory              = var.producer_memory

  environment_variables = {
    WIKI_CORPUS = var.wiki_corpus
  }
  # The producer publishes to the broker and reads staged chunks from the
  # database; it does not embed, so it needs no embedding key.
  secrets = merge(
    { RABBITMQ_URL = module.rabbitmq.url_secret_arn },
    local.rds_dsn_secret_arn != null ? { DATABASE_URL = local.rds_dsn_secret_arn } : {},
  )

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Observability for the ingestion pipeline. A scheduled lambda polls the broker's
# RabbitMQ management API (Amazon MQ exposes no per-queue metrics natively) and
# republishes per-versioned-queue backlog, publish rate and consumer count as
# custom CloudWatch metrics, plus a version-stripped rollup. A dashboard charts
# them next to the worker fleet. Gated off by default; enable it alongside the
# broker and worker to watch the queue. The lambda binary is built by
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
# enable flag. Worker widgets appear only when the embedding worker is running.
module "monitoring" {
  source = "../modules/monitoring"
  count  = var.enable_metrics_lambda ? 1 : 0

  project     = local.project
  environment = var.environment

  metrics_namespace = var.metrics_namespace
  broker_name       = local.broker_name
  queue_base        = "embedding.jobs"

  cluster_name        = module.ecs.cluster_name
  worker_service_name = var.enable_embed_worker && var.enable_rds ? module.embed_worker[0].service_name : ""
}

# Worker-lifecycle lambda: queue-depth autoscaling and zero-downtime rollout for
# the embedding-worker fleet, which runs under an EXTERNAL deployment controller.
# Built and validated now so the move of the workers onto ECS is turnkey; the
# fleet stays at zero (scaling max = 0) until then. Gated off by default; enable it
# alongside the broker and the worker. The lambda binary is built by
# `make lambda-workerlifecycle` in stack/backend before apply.

# The lambda's security group lives here (not in the module) so it can join the
# broker's management allow-list without a module dependency cycle, the same way
# the metrics-lambda SG is shared. Egress-only: it reaches the in-VPC broker and
# the AWS control plane (ECS, Secrets Manager, SSM) via the NAT gateway.
resource "aws_security_group" "worker_lifecycle" {
  count = var.enable_worker_lifecycle ? 1 : 0

  name        = "${local.project}-${var.environment}-workerlifecycle"
  description = "Worker-lifecycle lambda: egress only (RabbitMQ management API in-VPC, AWS APIs via NAT)."
  vpc_id      = module.vpc.vpc_id

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "${local.project}-${var.environment}-workerlifecycle" }
}

module "worker_lifecycle" {
  source = "../modules/worker-lifecycle"
  count  = var.enable_worker_lifecycle ? 1 : 0

  project     = local.project
  environment = var.environment

  source_binary_path = "${path.module}/../../backend/build/workerlifecycle/bootstrap"

  subnet_ids         = module.vpc.private_subnet_ids
  security_group_ids = [aws_security_group.worker_lifecycle[0].id]

  ecs_cluster_name        = module.ecs.cluster_name
  ecs_cluster_arn         = module.ecs.cluster_id
  task_role_arn           = module.iam.task_role_arn
  task_execution_role_arn = module.iam.task_execution_role_arn

  rabbitmq_url_secret_arn = module.rabbitmq.url_secret_arn

  # The worker tasks' own network, for a first-deploy task set when no PRIMARY
  # exists to copy it from.
  resource_prefix         = "${local.project}-${var.environment}"
  task_subnet_ids         = module.vpc.private_subnet_ids
  task_security_group_ids = [module.vpc.ecs_tasks_security_group_id]

  scaling_config = var.worker_lifecycle_scaling_config
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

  include_rds              = var.enable_rds
  include_scheduled_tasks  = var.enable_wiki_sync || var.enable_db_backup
  include_bastion          = var.enable_bastion
  include_metrics_lambda   = var.enable_metrics_lambda
  include_worker_lifecycle = var.enable_worker_lifecycle
}
