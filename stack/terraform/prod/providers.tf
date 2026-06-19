provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = "truth-in-stream"
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}

# us-east-1 alias for resources that must live there regardless of aws_region.
# ACM certificates fronting CloudFront are required to be in us-east-1, so the
# acm module is driven by this provider rather than the regional default.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"

  default_tags {
    tags = {
      Project     = "truth-in-stream"
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}
