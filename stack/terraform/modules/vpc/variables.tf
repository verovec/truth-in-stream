variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "vpc_cidr" {
  type        = string
  default     = "10.0.0.0/16"
  description = "CIDR block for the VPC."
}

variable "az_count" {
  type        = number
  default     = 2
  description = "Number of availability zones to span."
}

variable "nat_gateway_count" {
  type        = number
  description = "Number of NAT gateways. 1 = shared (cheaper, no AZ-redundant egress), az_count = per-AZ."

  validation {
    condition     = var.nat_gateway_count >= 1 && var.nat_gateway_count <= var.az_count
    error_message = "nat_gateway_count must be between 1 and az_count."
  }
}

variable "database_client_security_group_ids" {
  type        = list(string)
  default     = []
  description = "Extra security groups allowed to reach RDS on 5432, beyond the ECS tasks (which always have access). The SSM bastion's SG joins this list when provisioned so the operator's one-time DB load tunnel can reach the private database. Added as an inline ingress rule on the postgres SG (vs. a standalone aws_vpc_security_group_ingress_rule, which provider v6 forbids mixing with this SG's inline rules); an empty list adds no rule, so the SG is unchanged when no bastion exists."
}
