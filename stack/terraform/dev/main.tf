data "aws_caller_identity" "current" {}

locals {
  project           = "truth-in-stream"
  github_repository = "verovec/truth-in-stream"
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
  repositories = ["backend", "frontend", "migrate"]
}

module "rds" {
  source = "../modules/rds"

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

# External API keys. Terraform creates the containers only; set the values out
# of band (aws secretsmanager put-secret-value) before the first deploy.
resource "aws_secretsmanager_secret" "embedding_api_key" {
  name                    = "${local.project}/${var.environment}/app/embedding-api-key"
  description             = "Voyage AI API key. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret" "transcription_api_key" {
  name                    = "${local.project}/${var.environment}/app/transcription-api-key"
  description             = "ElevenLabs API key. Value set manually, never in Terraform."
  recovery_window_in_days = 0
}

module "iam" {
  source = "../modules/iam"

  project           = local.project
  environment       = var.environment
  github_repository = local.github_repository

  ecr_repository_arns = module.ecr.repository_arns
  cluster_arn         = module.ecs.cluster_id
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

  environment_variables = {
    PORT                    = "3000"
    NEXT_TELEMETRY_DISABLED = "1"
    # Same-origin: the ALB routes /api/* to the backend. NEXT_PUBLIC_ values
    # are baked at build time; this runtime copy covers server-side use.
    NEXT_PUBLIC_API_URL = ""
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
  health_check_path      = "/"
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
