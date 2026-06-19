variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "rate_limit" {
  type        = number
  default     = 2000
  description = "Per-IP request ceiling over the rate-based rule's evaluation window. A single client exceeding this many requests is blocked. The default is generous for a France-facing app: legitimate users stay well under it while it still throttles scrapers and floods. Tune up if a shared NAT egresses many real users."

  validation {
    condition     = var.rate_limit >= 100 && var.rate_limit <= 2000000000
    error_message = "rate_limit must be between 100 and 2000000000 (the WAFv2 rate-based statement bounds)."
  }
}

variable "rate_limit_window_seconds" {
  type        = number
  default     = 300
  description = "Evaluation window for the rate-based rule, in seconds. WAFv2 accepts 60, 120, 300, or 600; rate_limit is the request ceiling across this window."

  validation {
    condition     = contains([60, 120, 300, 600], var.rate_limit_window_seconds)
    error_message = "rate_limit_window_seconds must be one of 60, 120, 300, or 600."
  }
}

variable "managed_rule_groups" {
  type = list(object({
    name        = string
    priority    = number
    count_only  = optional(bool, false)
    vendor_name = optional(string, "AWS")
  }))
  description = "AWS managed rule groups to apply, in priority order. count_only puts a group in count mode (logs matches without blocking) so a new group can be tuned for false positives before it is enforced; leave it false to block."

  default = [
    { name = "AWSManagedRulesCommonRuleSet", priority = 10 },
    { name = "AWSManagedRulesKnownBadInputsRuleSet", priority = 20 },
    { name = "AWSManagedRulesSQLiRuleSet", priority = 30 },
    { name = "AWSManagedRulesAmazonIpReputationList", priority = 40 },
  ]
}

variable "log_retention_days" {
  type        = number
  default     = 30
  description = "Retention for the WAF decision log group in CloudWatch Logs."
}
