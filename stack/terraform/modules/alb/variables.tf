variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "public_subnet_ids" {
  type        = list(string)
  description = "Public subnets for the load balancer."
}

variable "security_group_id" {
  type        = string
  description = "Security group for the load balancer."
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
