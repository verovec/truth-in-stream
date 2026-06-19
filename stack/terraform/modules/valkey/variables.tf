variable "project" {
  type        = string
  description = "Project slug used to name the cache and its supporting resources."
}

variable "environment" {
  type        = string
  description = "Environment name (e.g. prod) used to namespace the cache resources."
}

variable "vpc_id" {
  type        = string
  description = "VPC the cache security group is created in."
}

variable "private_subnet_ids" {
  type        = list(string)
  description = "Private subnet IDs the cache subnet group spans. The cache has no public access."
}

variable "allowed_security_group_ids" {
  type        = list(string)
  description = "Security groups allowed to reach the cache on the Redis port (the backend task SG). Ingress is opened only from these."
}

variable "node_type" {
  type        = string
  default     = "cache.t4g.micro"
  description = "ElastiCache node class. A single small node fits a 24h ephemeral cache; a node failure causes only cache misses, never data loss."
}

variable "engine_version" {
  type        = string
  default     = "8.0"
  description = "Valkey engine version. ElastiCache Valkey is wire-compatible with the Redis client, so no application change is needed."
}

variable "parameter_group_name" {
  type        = string
  default     = "default.valkey8"
  description = "ElastiCache parameter group. Must match the Valkey major version (default.valkey8 for engine 8.x)."
}

variable "port" {
  type        = number
  default     = 6379
  description = "Port the cache listens on."
}
