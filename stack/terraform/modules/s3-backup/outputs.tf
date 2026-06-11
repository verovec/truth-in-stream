output "bucket_id" {
  value       = aws_s3_bucket.backups.id
  description = "Database backup bucket name; pass to the backup tooling as DB_BACKUP_BUCKET."
}

output "bucket_arn" {
  value       = aws_s3_bucket.backups.arn
  description = "Database backup bucket ARN, for a least-privilege backup policy."
}
