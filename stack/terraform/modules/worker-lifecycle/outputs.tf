output "function_names" {
  value       = { for k, fn in aws_lambda_function.fn : k => fn.function_name }
  description = "The three handler function names, keyed by handler (scale, cleanup, deploy)."
}

output "deploy_function_name" {
  value       = aws_lambda_function.fn["deploy"].function_name
  description = "Deploy handler function name. The deploy workflow invokes it to roll the worker fleet."
}

output "deploy_function_arn" {
  value       = aws_lambda_function.fn["deploy"].arn
  description = "Deploy handler ARN, granted to the deploy workflow's invoke permission."
}

output "role_arn" {
  value       = aws_iam_role.lambda.arn
  description = "Shared lambda execution role ARN."
}

output "scaling_config_parameter_name" {
  value       = aws_ssm_parameter.scaling_config.name
  description = "Parameter Store name holding the per-service scaling config."
}
