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
  value       = module.rds.endpoint
  description = "RDS endpoint (private; reachable from ECS tasks only)."
}

output "dsn_secret_arn" {
  value       = module.rds.dsn_secret_arn
  description = "Secrets Manager ARN holding DATABASE_URL."
}
