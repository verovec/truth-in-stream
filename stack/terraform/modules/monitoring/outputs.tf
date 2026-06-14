output "dashboard_name" {
  value       = aws_cloudwatch_dashboard.main.dashboard_name
  description = "CloudWatch dashboard name for the ingestion pipeline."
}

output "dashboard_arn" {
  value       = aws_cloudwatch_dashboard.main.dashboard_arn
  description = "CloudWatch dashboard ARN."
}
