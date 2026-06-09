output "deploy_role_arn" {
  value       = aws_iam_role.deploy.arn
  description = "Role GitHub Actions assumes to push images and deploy. Set as the AWS_DEPLOY_ROLE_ARN repo secret."
}

output "task_execution_role_arn" {
  value       = aws_iam_role.task_execution.arn
  description = "ECS task execution role."
}

output "task_role_arn" {
  value       = aws_iam_role.task.arn
  description = "ECS task (application) role."
}
