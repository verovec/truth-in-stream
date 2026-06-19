terraform {
  required_version = ">= 1.11.0, < 2.0.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
      # A CLOUDFRONT-scoped WAFv2 web ACL and its logging configuration must live
      # in us-east-1, so this module is always driven by an aliased provider the
      # caller pins to that region; it never uses the default (eu-west-3) provider.
      configuration_aliases = [aws.us_east_1]
    }
  }
}
