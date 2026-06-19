output "primary_endpoint_address" {
  value       = aws_elasticache_replication_group.main.primary_endpoint_address
  description = "DNS address of the primary cache node. Resolvable only inside the VPC."
}

output "port" {
  value       = aws_elasticache_replication_group.main.port
  description = "Port the cache listens on."
}

output "redis_url" {
  value       = "rediss://${aws_elasticache_replication_group.main.primary_endpoint_address}:${aws_elasticache_replication_group.main.port}"
  description = "Connection URL the backend consumes as REDIS_URL. rediss:// because transit encryption is required; no auth token, so it carries no secret."
}

output "security_group_id" {
  value       = aws_security_group.cache.id
  description = "Security group attached to the cache."
}
