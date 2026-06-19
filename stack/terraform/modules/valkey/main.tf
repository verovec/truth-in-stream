locals {
  name = "${var.project}-${var.environment}-valkey"
}

# Private subnet group: the cache is reachable only from inside the VPC, never
# from the internet. Spans every private subnet so the single node lands in one
# of the app's AZs.
resource "aws_elasticache_subnet_group" "main" {
  name       = local.name
  subnet_ids = var.private_subnet_ids

  tags = { Name = local.name }
}

# Cache security group: ingress on the Redis port only from the backend task SG,
# never the VPC at large. The egress block is required, not cosmetic: an
# aws_security_group with no egress block has NO egress (Terraform strips AWS's
# default allow-all), same gotcha called out for the Postgres SG in the terraform
# skill.
resource "aws_security_group" "cache" {
  name        = local.name
  description = "ElastiCache Valkey; ingress only from the backend task security group on the Redis port"
  vpc_id      = var.vpc_id

  ingress {
    description     = "Redis/Valkey from the backend tasks"
    from_port       = var.port
    to_port         = var.port
    protocol        = "tcp"
    security_groups = var.allowed_security_group_ids
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = local.name }
}

# Single-node Valkey replication group (cluster mode disabled). A replication
# group, not the legacy aws_elasticache_cluster, is the current best-practice
# resource for Valkey: it can grow a read replica later without replacement.
# num_cache_clusters = 1 means primary only (no replica, no automatic failover),
# which suits a 24h ephemeral analysis cache where a node loss only causes cache
# misses. Encryption: at-rest with AWS-managed keys (no customer KMS) and TLS
# in-transit, matching the RDS (sslmode) and RabbitMQ (amqps) data-plane baseline;
# the backend connects over rediss://. No auth token: there is no shared secret to
# manage, the cache is unreachable outside the VPC and locked to the backend SG,
# and TLS server-cert verification authenticates the endpoint.
resource "aws_elasticache_replication_group" "main" {
  replication_group_id = local.name
  description          = "Valkey analysis cache for ${var.project} ${var.environment}"

  engine               = "valkey"
  engine_version       = var.engine_version
  node_type            = var.node_type
  num_cache_clusters   = 1
  parameter_group_name = var.parameter_group_name
  port                 = var.port

  subnet_group_name  = aws_elasticache_subnet_group.main.name
  security_group_ids = [aws_security_group.cache.id]

  automatic_failover_enabled = false
  multi_az_enabled           = false

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  transit_encryption_mode    = "required"

  # An ephemeral cache: no snapshots to restore, a node loss is just cache misses.
  snapshot_retention_limit = 0

  auto_minor_version_upgrade = true
  apply_immediately          = true

  tags = { Name = local.name }
}
