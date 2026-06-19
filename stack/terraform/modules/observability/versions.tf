terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
      # The CLOUDFRONT-scoped WAF publishes its metrics in us-east-1, so the WAF
      # blocked-request alarm, its SNS topic, and a second forwarder copy are
      # driven by an aliased provider the caller pins to that region. The rest of
      # the module uses the default (regional) provider.
      configuration_aliases = [aws.us_east_1]
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
  }
}
