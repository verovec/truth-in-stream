locals {
  name = "${var.project}-${var.environment}-db-backups"
}

# Private bucket for database dumps (pg_dump custom-format archives). It holds
# the expensive-to-recompute embeddings, so the only access is via credentialed
# CLI; all public access is blocked and there is no CORS surface.
resource "aws_s3_bucket" "backups" {
  bucket = local.name
}

resource "aws_s3_bucket_ownership_controls" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket = aws_s3_bucket.backups.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Versioning is always on for backups: an overwritten or deleted dump stays
# recoverable for noncurrent_retention_days, so a bad backup cannot destroy the
# last good snapshot.
resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    id     = "expire-old-backups"
    status = "Enabled"

    filter {}

    expiration {
      days = var.retention_days
    }

    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_retention_days
    }
  }

  # Lifecycle's noncurrent rule requires versioning to exist first; serialize
  # after the public-access block too, so security settings land before the
  # lifecycle PUT and a first apply has no eventual-consistency race.
  depends_on = [
    aws_s3_bucket_versioning.backups,
    aws_s3_bucket_public_access_block.backups,
  ]
}
