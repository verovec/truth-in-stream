output "alerts_topic_arn" {
  value       = aws_sns_topic.alerts.arn
  description = "ARN of the regional alerts SNS topic every regional alarm publishes to."
}

output "forwarder_function_name" {
  value       = aws_lambda_function.forwarder.function_name
  description = "Name of the Slack forwarder Lambda."
}

output "forwarder_function_arn" {
  value       = aws_lambda_function.forwarder.arn
  description = "ARN of the Slack forwarder Lambda."
}

output "dashboard_name" {
  value       = var.create_dashboard ? aws_cloudwatch_dashboard.health[0].dashboard_name : null
  description = "CloudWatch health dashboard name, or null when create_dashboard is false."
}
