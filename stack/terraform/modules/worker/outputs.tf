output "service_name" {
  value       = aws_ecs_service.main.name
  description = "ECS service name (used by the deploy workflow)."
}

output "task_definition_family" {
  value       = aws_ecs_task_definition.main.family
  description = "Task definition family."
}
