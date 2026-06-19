# modules/waf

CLOUDFRONT-scoped WAFv2 web ACL for the app's CloudFront distribution.

CloudFront-scoped web ACLs (and their logging) must live in `us-east-1`, so the
module is always driven by the caller's `aws.us_east_1` aliased provider and
never touches the default regional provider:

```hcl
module "waf" {
  source = "../modules/waf"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  project     = local.project
  environment = var.environment
}
```

## What it creates

- An `aws_wafv2_web_acl` (`CLOUDFRONT` scope) with `default_action = allow`, so
  legitimate traffic passes untouched. Protection comes from:
  - **AWS managed rule groups** (`managed_rule_groups`): by default the common
    rule set, known-bad-inputs, SQLi, and the Amazon IP reputation list. Each
    entry can be set `count_only = true` to observe a group's matches without
    blocking, so a newly added group is tuned for false positives before it
    enforces.
  - **A per-IP rate-based rule** (`rate_limit` over `rate_limit_window_seconds`)
    that blocks any single client exceeding the ceiling.
- A CloudWatch log group named `aws-waf-logs-<project>-<env>-cloudfront` (the
  `aws-waf-logs-` prefix is mandatory) plus the `delivery.logs.amazonaws.com`
  resource policy, and an `aws_wafv2_web_acl_logging_configuration` that streams
  every decision there with the `authorization` and `cookie` headers redacted.

## Association

The module exposes `web_acl_arn`; the CloudFront module's `web_acl_id` input
takes that ARN (WAFv2 associations use the ARN despite the field name). The web
ACL is associated by setting `web_acl_id = module.waf.web_acl_arn` on the
`cloudfront` module call.

## Tuning

`rate_limit`, `rate_limit_window_seconds`, `managed_rule_groups`, and
`log_retention_days` are inputs, so thresholds and the rule set are adjustable
without editing the module.
