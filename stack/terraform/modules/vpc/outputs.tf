output "vpc_id" {
  value       = aws_vpc.main.id
  description = "VPC ID."
}

output "public_subnet_ids" {
  value       = aws_subnet.public[*].id
  description = "Public subnet IDs (one per AZ)."
}

output "private_subnet_ids" {
  value       = aws_subnet.private[*].id
  description = "Private subnet IDs (one per AZ)."
}

output "alb_security_group_id" {
  value       = aws_security_group.alb.id
  description = "Security group for the public ALB."
}

output "ecs_tasks_security_group_id" {
  value       = aws_security_group.ecs_tasks.id
  description = "Security group for Fargate tasks."
}

output "postgres_security_group_id" {
  value       = aws_security_group.postgres.id
  description = "Security group for the RDS instance."
}
