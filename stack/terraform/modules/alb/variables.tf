variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "internal" {
  type        = bool
  default     = false
  description = "When true, the ALB is internal (private subnets) and fronted by CloudFront via a VPC origin; the module owns a security group restricted to the CloudFront origin-facing prefix list. When false, the ALB is internet-facing in the public subnets using the passed-in security group."
}

variable "vpc_id" {
  type        = string
  default     = ""
  description = "VPC the load balancer lives in. Required only when internal = true (used for the module-owned restricted security group)."
}

variable "public_subnet_ids" {
  type        = list(string)
  default     = []
  description = "Public subnets for an internet-facing load balancer. Required when internal = false."
}

variable "private_subnet_ids" {
  type        = list(string)
  default     = []
  description = "Private subnets for an internal load balancer. Required when internal = true."
}

variable "security_group_id" {
  type        = string
  default     = ""
  description = "Security group for an internet-facing load balancer. Required when internal = false; ignored when internal = true (the module owns the restricted SG)."
}

variable "certificate_arn" {
  type        = string
  default     = ""
  description = "ACM certificate ARN. Empty serves plain HTTP; set once a domain exists to enable HTTPS + redirect."
}

variable "deletion_protection" {
  type        = bool
  default     = false
  description = "Protect the load balancer from deletion."
}
