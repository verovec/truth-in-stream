output "account_id" {
  value       = data.aws_caller_identity.current.account_id
  description = "Account this root operates against. Must be the main account (matches main_account_id)."
}

output "hosted_zone_name" {
  value       = data.aws_route53_zone.main.name
  description = "Name of the resolved hosted zone, as a sanity check that the right zone was looked up."
}

output "acm_validation_record_fqdns" {
  value       = [for r in aws_route53_record.acm_validation : r.fqdn]
  description = "FQDNs of the ACM DNS-validation CNAMEs created in the main-account zone."
}

output "alias_record_fqdns" {
  value       = concat([for r in aws_route53_record.alias_a : r.fqdn], [for r in aws_route53_record.alias_aaaa : r.fqdn])
  description = "FQDNs of the apex/www CloudFront alias records (A and AAAA)."
}

output "cloudfront_alias_target" {
  value       = local.cloudfront_domain_name
  description = "The CloudFront domain the apex/www aliases resolve to."
}
