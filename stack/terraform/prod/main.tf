data "aws_caller_identity" "current" {}

locals {
  project           = "truth-in-stream"
  github_repository = "verovec/truth-in-stream"
}

module "vpc" {
  source = "../modules/vpc"

  project     = local.project
  environment = var.environment
  # Per-AZ NAT: AZ-redundant egress for production.
  nat_gateway_count = 2
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
  repositories = ["backend", "frontend", "migrate"]
}

module "rds" {
  source = "../modules/rds"

  project     = local.project
  environment = var.environment

  instance_class        = "db.t4g.small"
  multi_az              = true
  deletion_protection   = true
  skip_final_snapshot   = false
  backup_retention_days = 21

  private_subnet_ids = module.vpc.private_subnet_ids
  security_group_id  = module.vpc.postgres_security_group_id
}

# External API keys. Terraform creates the containers only; set the values out
# of band (aws secretsmanager put-secret-value) before the first deploy.
resource "aws_secretsmanager_secret" "embedding_api_key" {
  name        = "${local.project}/${var.environment}/app/embedding-api-key"
  description = "Voyage AI API key. Value set manually, never in Terraform."
}

resource "aws_secretsmanager_secret" "transcription_api_key" {
  name        = "${local.project}/${var.environment}/app/transcription-api-key"
  description = "AssemblyAI API key. Value set manually, never in Terraform."
}

module "media_storage" {
  source = "../modules/s3"

  project     = local.project
  environment = var.environment

  cors_allowed_origins = var.media_cors_allowed_origins
}

module "iam" {
  source = "../modules/iam"

  project           = local.project
  environment       = var.environment
  github_repository = local.github_repository
  # The OIDC provider is account-global and owned by the dev environment.
  create_oidc_provider = false

  ecr_repository_arns = module.ecr.repository_arns
  cluster_arn         = module.ecs.cluster_id
  media_bucket_arn    = module.media_storage.bucket_arn
  secret_arns = [
    module.rds.dsn_secret_arn,
    aws_secretsmanager_secret.embedding_api_key.arn,
    aws_secretsmanager_secret.transcription_api_key.arn,
  ]
  ssm_parameter_arns = [
    aws_ssm_parameter.private_subnet_ids.arn,
    aws_ssm_parameter.tasks_security_group_id.arn,
  ]
}

module "alb" {
  source = "../modules/alb"

  project     = local.project
  environment = var.environment

  public_subnet_ids   = module.vpc.public_subnet_ids
  security_group_id   = module.vpc.alb_security_group_id
  deletion_protection = true
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
  desired_count  = 2

  environment_variables = {
    PORT = "8080"
    # Object storage uses the task role for credentials (no endpoint, no static
    # keys); only the bucket and region are configured here.
    STORAGE_BUCKET = module.media_storage.bucket_id
    STORAGE_REGION = var.aws_region
  }
  secrets = {
    DATABASE_URL          = module.rds.dsn_secret_arn
    EMBEDDING_API_KEY     = aws_secretsmanager_secret.embedding_api_key.arn
    TRANSCRIPTION_API_KEY = aws_secretsmanager_secret.transcription_api_key.arn
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
  desired_count  = 2

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

module "migration" {
  source = "../modules/migration"

  project     = local.project
  environment = var.environment

  image                   = "${module.ecr.repository_urls["migrate"]}:latest"
  dsn_secret_arn          = module.rds.dsn_secret_arn
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
}

# Weekly Wikipedia delta sync as a one-shot scheduled Fargate task.
module "wiki_sync" {
  source = "../modules/scheduled-task"
  count  = var.enable_wiki_sync ? 1 : 0

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
  secrets = {
    DATABASE_URL      = module.rds.dsn_secret_arn
    EMBEDDING_API_KEY = aws_secretsmanager_secret.embedding_api_key.arn
  }

  cluster_arn             = module.ecs.cluster_id
  subnet_ids              = module.vpc.private_subnet_ids
  security_group_ids      = [module.vpc.ecs_tasks_security_group_id]
  task_execution_role_arn = module.iam.task_execution_role_arn
  task_role_arn           = module.iam.task_role_arn
  log_group_name          = module.ecs.log_group_name
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
