locals {
  name = "${var.project}-${var.environment}-media"
}

# Private bucket for uploaded video. Uploads and playback go direct from the
# browser via presigned URLs, so the only public surface is CORS (below); the
# bucket itself blocks all public access.
resource "aws_s3_bucket" "media" {
  bucket = local.name
}

# BucketOwnerEnforced disables ACLs entirely: object ownership is unambiguous
# and presigned PUTs cannot set a public-read ACL.
resource "aws_s3_bucket_ownership_controls" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "media" {
  bucket = aws_s3_bucket.media.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_versioning" "media" {
  bucket = aws_s3_bucket.media.id

  versioning_configuration {
    status = var.versioning_enabled ? "Enabled" : "Suspended"
  }
}

# Browser-issued presigned PUT (upload) and GET (playback, with range requests)
# require CORS. Origins are restricted to the frontend; ETag is exposed so the
# uploader can confirm the stored object.
resource "aws_s3_bucket_cors_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  cors_rule {
    allowed_methods = ["PUT", "GET", "HEAD"]
    allowed_origins = var.cors_allowed_origins
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3000
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  # Reclaim storage from uploads the browser abandoned mid-multipart.
  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  dynamic "rule" {
    for_each = var.expiration_days > 0 ? [1] : []

    content {
      id     = "expire-objects"
      status = "Enabled"

      filter {}

      expiration {
        days = var.expiration_days
      }
    }
  }

  # Lifecycle config races bucket creation; serialize behind versioning so the
  # bucket is fully settled first.
  depends_on = [aws_s3_bucket_versioning.media]
}
