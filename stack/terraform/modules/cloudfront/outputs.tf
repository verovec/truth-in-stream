output "distribution_id" {
  value       = aws_cloudfront_distribution.main.id
  description = "CloudFront distribution id."
}

output "distribution_arn" {
  value       = aws_cloudfront_distribution.main.arn
  description = "CloudFront distribution ARN."
}

output "domain_name" {
  value       = aws_cloudfront_distribution.main.domain_name
  description = "CloudFront distribution domain name (the *.cloudfront.net target). The main-account hosted zone (VER-140) points the apex/www alias records at this."
}

output "hosted_zone_id" {
  value       = aws_cloudfront_distribution.main.hosted_zone_id
  description = "CloudFront's fixed hosted-zone id for Route 53 alias records. The main-account alias records (VER-140) need this alongside domain_name."
}
