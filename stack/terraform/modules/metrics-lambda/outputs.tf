output "function_name" {
  value       = aws_lambda_function.main.function_name
  description = "Metrics-poller lambda function name."
}

output "function_arn" {
  value       = aws_lambda_function.main.arn
  description = "Metrics-poller lambda ARN."
}

output "role_arn" {
  value       = aws_iam_role.lambda.arn
  description = "Lambda execution role ARN."
}
