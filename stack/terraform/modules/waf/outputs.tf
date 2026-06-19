output "web_acl_arn" {
  value       = aws_wafv2_web_acl.this.arn
  description = "ARN of the CloudFront-scoped web ACL. CloudFront's web_acl_id takes the ARN (not the id) for WAFv2 associations."
}

output "web_acl_id" {
  value       = aws_wafv2_web_acl.this.id
  description = "Id of the web ACL."
}

output "log_group_name" {
  value       = aws_cloudwatch_log_group.waf.name
  description = "CloudWatch Logs group receiving WAF decision logs."
}
