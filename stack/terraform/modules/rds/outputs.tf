output "endpoint" {
  value       = aws_db_instance.main.endpoint
  description = "host:port of the instance."
}

output "credentials_secret_arn" {
  value       = aws_secretsmanager_secret.credentials.arn
  description = "Structured credentials (break-glass / admin tooling). The application reads dsn_secret_arn, not this."
}

output "dsn_secret_arn" {
  value       = aws_secretsmanager_secret.dsn.arn
  description = "Secrets Manager ARN holding the DATABASE_URL connection string."
}
