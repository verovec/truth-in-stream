output "bucket_id" {
  value       = aws_s3_bucket.media.id
  description = "Media bucket name."
}

output "bucket_arn" {
  value       = aws_s3_bucket.media.arn
  description = "Media bucket ARN, for the least-privilege task-role policy."
}

output "bucket_regional_domain_name" {
  value       = aws_s3_bucket.media.bucket_regional_domain_name
  description = "Regional domain name of the media bucket."
}
