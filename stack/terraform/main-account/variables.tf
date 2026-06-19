variable "aws_region" {
  type        = string
  default     = "eu-west-3"
  description = "Region the provider operates in. Route 53 is global, but the provider still needs a region; matches the rest of the stack."
}

variable "main_account_id" {
  type        = string
  default     = "040265332493"
  description = "AWS account that owns the authoritative hosted zone for jeminforme.fr. Used as an allowed_account_ids guard so this root can only ever write into the main account."
}

variable "main_account_role_arn" {
  type        = string
  default     = ""
  description = "IAM role ARN to assume in the main account. Leave empty to use ambient credentials (operator already authenticated as the main account); set it for the cross-account assume-role path."
}

variable "hosted_zone_id" {
  type        = string
  default     = "Z0839748310ZNBMJ0HI90"
  description = "The authoritative public hosted zone for jeminforme.fr in the main account. Looked up by id (deterministic; no public/private ambiguity)."
}

variable "domain_name" {
  type        = string
  default     = "jeminforme.fr"
  description = "Apex domain. The apex A/AAAA aliases use this; the www aliases use www.<domain_name>."
}

# --- Source of the app-account values ---------------------------------------
# The records this root creates are derived from the app account's prod outputs:
# the ACM validation CNAMEs and the CloudFront alias target. Two ways to supply
# them, selected by read_remote_state:
#   true  -> read the app-account prod state directly (requires cross-account S3
#            read on the state bucket).
#   false -> the operator pastes the three values into tfvars from
#            `terraform -chdir=stack/terraform/prod output`.

variable "read_remote_state" {
  type        = bool
  default     = true
  description = "When true, read the app-account prod outputs via terraform_remote_state (needs cross-account S3 read on the state bucket). When false, consume the *_override variables the operator pastes from the prod outputs."
}

variable "app_state_bucket" {
  type        = string
  default     = "truth-in-stream-tfstate"
  description = "S3 bucket holding the app-account prod state (read when read_remote_state is true)."
}

variable "app_state_key" {
  type        = string
  default     = "prod/terraform.tfstate"
  description = "State key for the app-account prod root (read when read_remote_state is true)."
}

variable "app_state_region" {
  type        = string
  default     = "eu-west-3"
  description = "Region of the app-account prod state bucket (read when read_remote_state is true)."
}

variable "certificate_domain_validation_options_override" {
  type = map(object({
    name  = string
    type  = string
    value = string
  }))
  default     = {}
  description = "Fallback for read_remote_state=false: the prod output certificate_domain_validation_options, a map keyed by domain of the {name,type,value} CNAME validation records. Nothing here is secret."
}

variable "cloudfront_domain_name_override" {
  type        = string
  default     = ""
  description = "Fallback for read_remote_state=false: the prod output cloudfront_domain_name (the *.cloudfront.net alias target)."
}

variable "cloudfront_hosted_zone_id_override" {
  type        = string
  default     = ""
  description = "Fallback for read_remote_state=false: the prod output cloudfront_hosted_zone_id (CloudFront's fixed Route 53 alias zone id, Z2FDTNDATAQYW2)."

  validation {
    # CloudFront's alias hosted-zone id is a global AWS constant. Catch a wrong
    # paste (e.g. the app-account zone id) before apply rejects it obscurely.
    condition     = var.cloudfront_hosted_zone_id_override == "" || var.cloudfront_hosted_zone_id_override == "Z2FDTNDATAQYW2"
    error_message = "cloudfront_hosted_zone_id_override must be CloudFront's fixed alias zone id Z2FDTNDATAQYW2 (or empty when read_remote_state is true)."
  }
}
