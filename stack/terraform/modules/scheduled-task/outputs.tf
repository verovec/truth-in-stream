output "task_definition_arn" {
  value       = aws_ecs_task_definition.main.arn
  description = "ARN of the task definition. Pass it to `aws ecs run-task` to launch the task on demand (the only way to run it when schedule_expression is empty)."
}

output "task_definition_family" {
  value       = aws_ecs_task_definition.main.family
  description = "Task definition family, for `aws ecs run-task --task-definition <family>` (always uses the latest revision)."
}

output "container_name" {
  value       = var.name
  description = "Container name in the task definition, for run-task container overrides."
}
