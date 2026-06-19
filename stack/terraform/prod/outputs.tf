output "account_id" {
  value       = data.aws_caller_identity.current.account_id
  description = "AWS account ID Terraform is operating against."
}

output "app_url" {
  value       = "https://${var.domain_name}"
  description = "Public application URL: HTTPS at the apex domain via CloudFront. Resolves once the main-account hosted zone (VER-140) points the apex/www alias records at the distribution."
}

output "cloudfront_distribution_id" {
  value       = module.cloudfront.distribution_id
  description = "CloudFront distribution id."
}

output "cloudfront_distribution_arn" {
  value       = module.cloudfront.distribution_arn
  description = "CloudFront distribution ARN."
}

output "cloudfront_domain_name" {
  value       = module.cloudfront.domain_name
  description = "CloudFront distribution domain name. The main-account hosted zone (VER-140) points the apex/www alias records at this."
}

output "cloudfront_hosted_zone_id" {
  value       = module.cloudfront.hosted_zone_id
  description = "CloudFront's fixed hosted-zone id for Route 53 alias records (VER-140)."
}

output "waf_web_acl_arn" {
  value       = module.waf.web_acl_arn
  description = "ARN of the CLOUDFRONT-scoped WAFv2 web ACL associated with the distribution."
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
  description = "Deploy role for this environment."
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
  description = "SSM port-forward target instance ID for the one-time DB load. Null when enable_bastion is false."
}

output "certificate_arn" {
  value       = module.acm.certificate_arn
  description = "ARN of the us-east-1 ACM certificate for the domain. Consumed by CloudFront and referenced by the main-account DNS root."
}

output "certificate_domain_validation_options" {
  value       = module.acm.domain_validation_options
  description = "DNS validation records (CNAME name/type/value per domain) the main-account hosted zone must create for the certificate to reach ISSUED. No secret; safe to publish for the main-account root to consume."
}

output "apply_required_actions" {
  value       = module.apply_permissions.actions
  description = "IAM actions the apply role must hold to provision this environment. The pre-apply guard reads this from the plan and fails before apply if the role is missing any."
}

output "alerts_topic_arn" {
  value       = module.observability.alerts_topic_arn
  description = "SNS topic CloudWatch alarms publish to; the Slack forwarder Lambda is its subscriber."
}

output "health_dashboard_name" {
  value       = module.observability.dashboard_name
  description = "CloudWatch dashboard summarising the key health signals (ALB, targets, ECS tasks, RDS, MQ, WAF)."
}
