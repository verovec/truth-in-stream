output "broker_id" {
  value       = aws_mq_broker.main.id
  description = "Amazon MQ broker ID."
}

output "amqp_endpoint" {
  value       = local.amqp_endpoint
  description = "Broker AMQPS endpoint (private; reachable from the allowed security groups only)."
}

output "url_secret_arn" {
  value       = aws_secretsmanager_secret.url.arn
  description = "Secrets Manager ARN holding the full RABBITMQ_URL connection string."
}

output "security_group_id" {
  value       = aws_security_group.broker.id
  description = "Security group fronting the broker."
}
