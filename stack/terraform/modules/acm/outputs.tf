output "certificate_arn" {
  value       = aws_acm_certificate.this.arn
  description = "ARN of the requested certificate. Consumed by the CloudFront distribution and by the main-account DNS root for reference."
}

output "domain_validation_options" {
  # Keyed by domain so the main-account root can create one CNAME per name. The
  # set is deduplicated by ACM (apex and a www SAN that share a record collapse
  # to a single option), so keying on domain_name is stable.
  value = {
    for dvo in aws_acm_certificate.this.domain_validation_options : dvo.domain_name => {
      name  = dvo.resource_record_name
      type  = dvo.resource_record_type
      value = dvo.resource_record_value
    }
  }
  description = "DNS validation records (CNAME name/type/value per domain) the main-account hosted zone must create for the certificate to reach ISSUED."
}
