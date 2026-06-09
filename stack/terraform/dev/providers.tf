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
