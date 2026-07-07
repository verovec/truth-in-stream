output "service_name" {
  value       = aws_ecs_service.main.name
  description = "ECS service name (used by the deploy workflow)."
}

output "task_definition_family" {
  value       = aws_ecs_task_definition.main.family
  description = "Task definition family."
}

output "target_group_arn_suffix" {
  value       = aws_lb_target_group.main.arn_suffix
  description = "ARN suffix of the target group, the TargetGroup dimension value on AWS/ApplicationELB CloudWatch metrics."
}
