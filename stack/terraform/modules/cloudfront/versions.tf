terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    aws = {
      source = "hashicorp/aws"
      # aws_cloudfront_vpc_origin is a v6-era resource; pin to ~> 6.0 so a
      # consumer on an older provider fails at init with a clear constraint
      # error rather than an "unsupported resource" error at plan time.
      version = "~> 6.0"
    }
  }
}
