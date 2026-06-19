# main-account DNS root (manual apply, CI-excluded)

This is a **separate terraform root** that publishes the app's public DNS into the
authoritative hosted zone for `jeminforme.fr`, which lives in the **main account**
(`040265332493`, zone `Z0839748310ZNBMJ0HI90`). The app — CloudFront and the ACM
certificate — lives in the **app account** (`965638922723`).

It creates two things, so no record is ever clicked in by hand:

1. The **ACM DNS-validation CNAMEs** for the app-account certificate (from the
   prod output `certificate_domain_validation_options`). Once these resolve, the
   certificate moves from `PENDING_VALIDATION` to `ISSUED`.
2. The **apex and `www` alias A/AAAA records** pointing at the CloudFront
   distribution (from the prod outputs `cloudfront_domain_name` +
   `cloudfront_hosted_zone_id`).

It has its own state key (`main-account/terraform.tfstate`), so it never conflicts
with the prod chain's state.

## Not in CI — the operator applies this by hand

This root targets the **main account** and is **deliberately excluded from CI**.
The terraform CI workflow (`.github/workflows/terraform.yml`) lists each root it
runs by explicit path — only `stack/terraform/dev` plans/applies automatically;
`prod` is promoted by hand, and this root is **not listed at all**, so no CI job
ever runs `plan` or `apply` against the main account. Do not add it to that
workflow. `fmt`/`validate` are still exercised locally and in code review.

## Where the app-account values come from

Selected by `read_remote_state`:

- **`true` (default)** — reads the app-account prod state directly via
  `terraform_remote_state`. Needs cross-account S3 read on
  `truth-in-stream-tfstate`.
- **`false`** — the operator pastes the three prod outputs into `terraform.tfvars`
  (`*_override` variables). Use this when cross-account S3 read is not granted.
  A precondition fails the plan if the overrides are missing.

## Apply runbook (operator only)

Apply order is **app account first, main account second**:

1. **App account** (`prod` root): `terraform apply` requests the certificate
   (created `PENDING_VALIDATION`) and provisions CloudFront. This does not block
   on DNS.
2. **Read the values to publish** (from `stack/terraform/prod`):

   ```sh
   terraform -chdir=../prod output certificate_domain_validation_options
   terraform -chdir=../prod output cloudfront_domain_name
   terraform -chdir=../prod output cloudfront_hosted_zone_id
   ```

   With `read_remote_state = true` this happens automatically; otherwise paste
   them into `terraform.tfvars` (see `terraform.tfvars.example`).
3. **Main account** (this root):

   ```sh
   make tf-main-account-plan    # review, no changes applied
   make tf-main-account-apply   # operator runs the apply by hand
   ```

   or directly:

   ```sh
   cd stack/terraform/main-account
   terraform init
   terraform plan
   terraform apply
   ```

4. **Verify** (no console clicks): the certificate reaches `ISSUED`, and the apex
   and `www` records resolve to CloudFront:

   ```sh
   aws acm describe-certificate --region us-east-1 \
     --certificate-arn "$(terraform -chdir=../prod output -raw certificate_arn)" \
     --query 'Certificate.Status'
   dig +short jeminforme.fr
   dig +short www.jeminforme.fr
   ```

The registrar is assumed to already delegate to this zone's nameservers. If not,
the operator sets them once at the registrar — that is a registrar action, not an
AWS one.
