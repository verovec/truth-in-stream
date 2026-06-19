# Public TLS certificate for CloudFront, requested in us-east-1 (the region ACM
# certs for CloudFront must live in). DNS validation, but this module does NOT
# create the validation records and does NOT wait for issuance: the authoritative
# hosted zone for the domain lives in a different AWS account (the main account),
# so the records are created there by a separate terraform root (VER-140) reading
# this module's domain_validation_options output. The certificate stays
# PENDING_VALIDATION until those cross-account records exist; there is
# deliberately no aws_acm_certificate_validation resource here, since it would
# block forever on records this account cannot create.
resource "aws_acm_certificate" "this" {
  provider = aws.us_east_1

  domain_name               = var.domain_name
  subject_alternative_names = var.subject_alternative_names
  validation_method         = "DNS"

  tags = var.tags

  # ACM cannot mutate a certificate's domain set in place; replacing it requires
  # the new cert to exist before the old one is destroyed so dependents never
  # reference a deleted ARN.
  lifecycle {
    create_before_destroy = true
  }
}
