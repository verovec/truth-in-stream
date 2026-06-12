output "account_id" {
  value       = data.aws_caller_identity.current.account_id
  description = "AWS account ID Terraform is operating against."
}

output "app_url" {
  value       = "http://${module.alb.dns_name}"
  description = "Public application URL (ALB DNS, HTTP until a domain exists)."
}

output "ecr_repository_urls" {
  value       = module.ecr.repository_urls
  description = "ECR repositories the deploy workflow pushes to."
}

output "ecs_cluster_name" {
  value       = module.ecs.cluster_name
  description = "ECS cluster name."
}

output "deploy_role_arn" {
  value       = module.iam.deploy_role_arn
  description = "Set as the AWS_DEPLOY_ROLE_ARN GitHub repository secret."
}

output "rds_endpoint" {
  value       = one(module.rds[*].endpoint)
  description = "RDS endpoint (private; reachable from ECS tasks only). Null when enable_rds is false."
}

output "dsn_secret_arn" {
  value       = local.rds_dsn_secret_arn
  description = "Secrets Manager ARN holding DATABASE_URL. Null when enable_rds is false."
}

output "rabbitmq_url_secret_arn" {
  value       = module.rabbitmq.url_secret_arn
  description = "Secrets Manager ARN holding RABBITMQ_URL for the embedding-job queue."
}

output "db_backup_bucket" {
  value       = module.db_backup_storage.bucket_id
  description = "Database backup bucket; export as DB_BACKUP_BUCKET for make backup/restore."
}

output "bastion_instance_id" {
  value       = one(module.bastion[*].instance_id)
  description = "SSM port-forward target instance ID. Null when enable_bastion is false."
}

output "apply_required_actions" {
  value       = module.apply_permissions.actions
  description = "IAM actions the apply role must hold to provision this environment. The pre-apply guard reads this from the plan and fails before apply if the role is missing any."
}
