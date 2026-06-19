data "aws_caller_identity" "current" {}

# The authoritative zone for jeminforme.fr in the main account. Looked up by id
# (deterministic; avoids public/private name ambiguity). This data source also
# confirms the assumed identity can actually see the zone.
data "aws_route53_zone" "main" {
  zone_id = var.hosted_zone_id
}

# Read the app-account prod outputs when granted cross-account S3 read. count
# gates it so read_remote_state=false never touches the app-account state.
data "terraform_remote_state" "app" {
  count   = var.read_remote_state ? 1 : 0
  backend = "s3"
  config = {
    bucket = var.app_state_bucket
    key    = var.app_state_key
    region = var.app_state_region
  }
}

locals {
  # Select the source of the app-account values: live remote state, or the
  # operator-pasted override variables.
  validation_options = var.read_remote_state ? data.terraform_remote_state.app[0].outputs.certificate_domain_validation_options : var.certificate_domain_validation_options_override

  cloudfront_domain_name    = var.read_remote_state ? data.terraform_remote_state.app[0].outputs.cloudfront_domain_name : var.cloudfront_domain_name_override
  cloudfront_hosted_zone_id = var.read_remote_state ? data.terraform_remote_state.app[0].outputs.cloudfront_hosted_zone_id : var.cloudfront_hosted_zone_id_override

  # The records the apex/www aliases are created for.
  alias_fqdns = [var.domain_name, "www.${var.domain_name}"]

  # In the tfvars path (read_remote_state = false) the operator MUST paste all
  # three prod outputs in; otherwise the selected source is empty and the plan
  # would silently create zero / broken records. Guarded by preconditions
  # attached to the record resources below, which Terraform evaluates before
  # planning each resource, so a missing paste fails the plan with a clear
  # message instead of producing an empty/broken record set.
  source_ready = var.read_remote_state || (length(var.certificate_domain_validation_options_override) > 0 && var.cloudfront_domain_name_override != "" && var.cloudfront_hosted_zone_id_override != "")

  source_error = "read_remote_state is false, so certificate_domain_validation_options_override, cloudfront_domain_name_override, and cloudfront_hosted_zone_id_override must all be set (paste them from `terraform -chdir=../prod output`)."
}

# (1) ACM DNS-validation records. One CNAME per validation option. The set is
# already deduplicated by ACM upstream (apex + www collapse to one record when
# they share a name), so iterating the map is safe. allow_overwrite tolerates a
# record already present in the zone from a previous request.
resource "aws_route53_record" "acm_validation" {
  for_each = local.validation_options

  zone_id         = data.aws_route53_zone.main.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.value]
  ttl             = 60
  allow_overwrite = true

  lifecycle {
    precondition {
      condition     = local.source_ready
      error_message = local.source_error
    }
  }
}

# (2) Apex + www alias A/AAAA records pointing at the CloudFront distribution.
# evaluate_target_health is false: Route 53 does not health-check CloudFront
# alias targets. No ttl on alias records (Route 53 ignores it).
resource "aws_route53_record" "alias_a" {
  for_each = toset(local.alias_fqdns)

  zone_id = data.aws_route53_zone.main.zone_id
  name    = each.value
  type    = "A"

  alias {
    name                   = local.cloudfront_domain_name
    zone_id                = local.cloudfront_hosted_zone_id
    evaluate_target_health = false
  }

  lifecycle {
    precondition {
      condition     = local.source_ready
      error_message = local.source_error
    }
  }
}

resource "aws_route53_record" "alias_aaaa" {
  for_each = toset(local.alias_fqdns)

  zone_id = data.aws_route53_zone.main.zone_id
  name    = each.value
  type    = "AAAA"

  alias {
    name                   = local.cloudfront_domain_name
    zone_id                = local.cloudfront_hosted_zone_id
    evaluate_target_health = false
  }

  lifecycle {
    precondition {
      condition     = local.source_ready
      error_message = local.source_error
    }
  }
}
