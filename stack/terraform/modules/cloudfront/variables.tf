variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "alb_arn" {
  type        = string
  description = "ARN of the internal ALB the VPC origin points at."
}

variable "alb_dns_name" {
  type        = string
  description = "DNS name of the internal ALB; the distribution origin's domain_name."
}

variable "certificate_arn" {
  type        = string
  description = "ARN of the us-east-1 ACM certificate covering the aliases. CloudFront requires the certificate to live in us-east-1."
}

variable "aliases" {
  type        = list(string)
  description = "Alternate domain names served by the distribution (e.g. the apex and www). Must be covered by the certificate."
}

variable "origin_protocol_policy" {
  type        = string
  default     = "http-only"
  description = "How CloudFront connects to the ALB origin. The ALB terminates client TLS at CloudFront; the VPC-origin hop to the internal ALB defaults to http-only (private path, no public exposure). Set to https-only once the ALB serves a matching certificate on 443."

  validation {
    condition     = contains(["http-only", "https-only", "match-viewer"], var.origin_protocol_policy)
    error_message = "origin_protocol_policy must be one of http-only, https-only, or match-viewer."
  }
}

variable "price_class" {
  type        = string
  default     = "PriceClass_100"
  description = "CloudFront price class. PriceClass_100 (US/Canada/Europe) suits a France-facing app; widen if traffic globalizes."
}
