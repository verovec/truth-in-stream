locals {
  name      = "${var.project}-${var.environment}"
  origin_id = "${local.name}-alb-vpc-origin"

  # AWS managed policies for dynamic, never-cached app traffic. CachingDisabled
  # sets every TTL to 0; AllViewer forwards all headers (including Host, which
  # the ALB listener rules match on), cookies, and query strings to the origin.
  cache_policy_caching_disabled    = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  origin_request_policy_all_viewer = "216adef6-5c7f-47e4-b989-5492eafa07d3"
  allowed_methods                  = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
  cached_methods                   = ["GET", "HEAD"]
}

# PrivateLink endpoint that lets CloudFront reach the internal ALB without any
# public exposure. Referenced by the distribution's origin via vpc_origin_id.
resource "aws_cloudfront_vpc_origin" "alb" {
  vpc_origin_endpoint_config {
    name                   = local.origin_id
    arn                    = var.alb_arn
    http_port              = 80
    https_port             = 443
    origin_protocol_policy = var.origin_protocol_policy

    origin_ssl_protocols {
      items    = ["TLSv1.2"]
      quantity = 1
    }
  }
}

resource "aws_cloudfront_distribution" "main" {
  enabled         = true
  is_ipv6_enabled = true
  aliases         = var.aliases
  price_class     = var.price_class
  comment         = "${local.name} app distribution (internal ALB via VPC origin)"
  # WAFv2 association: this field takes the web ACL ARN. null leaves it
  # unassociated.
  web_acl_id = var.web_acl_id

  origin {
    domain_name = var.alb_dns_name
    origin_id   = local.origin_id

    vpc_origin_config {
      vpc_origin_id = aws_cloudfront_vpc_origin.alb.id
    }
  }

  # Default behavior -> the frontend service. Dynamic: caching disabled, all
  # viewer headers/cookies/query forwarded to the origin.
  default_cache_behavior {
    target_origin_id       = local.origin_id
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = local.allowed_methods
    cached_methods         = local.cached_methods
    compress               = true

    cache_policy_id          = local.cache_policy_caching_disabled
    origin_request_policy_id = local.origin_request_policy_all_viewer
  }

  # /api/* -> the backend service. Same dynamic, never-cached treatment.
  ordered_cache_behavior {
    path_pattern           = "/api/*"
    target_origin_id       = local.origin_id
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = local.allowed_methods
    cached_methods         = local.cached_methods
    compress               = true

    cache_policy_id          = local.cache_policy_caching_disabled
    origin_request_policy_id = local.origin_request_policy_all_viewer
  }

  viewer_certificate {
    acm_certificate_arn      = var.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }
}
