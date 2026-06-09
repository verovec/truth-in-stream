output "cluster_id" {
  value       = aws_ecs_cluster.main.id
  description = "ECS cluster ID (ARN)."
}

output "cluster_name" {
  value       = aws_ecs_cluster.main.name
  description = "ECS cluster name."
}

output "log_group_name" {
  value       = aws_cloudwatch_log_group.ecs.name
  description = "CloudWatch log group for task logs."
}
