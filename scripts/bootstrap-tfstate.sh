#!/usr/bin/env bash
# Create and configure the S3 bucket that backs Terraform remote state, before
# the first `terraform init`. Native S3 state locking (use_lockfile = true)
# requires the bucket to exist with versioning enabled; this script also locks
# the bucket down with default encryption and a full public-access block.
#
# Idempotent: safe to re-run. It creates the bucket only when missing and then
# (re)asserts versioning, encryption, and the public-access block every time.
#
# The bucket is shared by every environment (dev/, prod/) via per-env state
# keys, so it is bootstrapped once for the account. All operator tooling targets
# the account through one AWS SSO profile; set AWS_PROFILE to it (see
# stack/terraform/README.md). In CI, OIDC supplies credentials and AWS_PROFILE
# is left unset.
#
# Env overrides:
#   STATE_BUCKET  state bucket name      (default truth-in-stream-tfstate)
#   AWS_REGION    bucket region          (default eu-west-3)
#   AWS_PROFILE   AWS SSO profile name   (default truth-in-stream-dev; unset to
#                                         use ambient credentials, e.g. in CI)
set -euo pipefail

STATE_BUCKET="${STATE_BUCKET:-truth-in-stream-tfstate}"
AWS_REGION="${AWS_REGION:-eu-west-3}"
# Default to the documented dev SSO profile, but allow an explicit empty value
# (AWS_PROFILE=) to fall back to ambient credentials.
AWS_PROFILE="${AWS_PROFILE-truth-in-stream-dev}"

# aws_args holds the shared --profile/--region flags so every call targets the
# same account and region. --profile is omitted when AWS_PROFILE is empty.
aws_args=(--region "$AWS_REGION")
if [ -n "$AWS_PROFILE" ]; then
	aws_args+=(--profile "$AWS_PROFILE")
fi

log() { printf '%s\n' "$*" >&2; }

# Confirm we have a usable session; for an expired SSO profile, prompt a login.
ensure_session() {
	if aws "${aws_args[@]}" sts get-caller-identity >/dev/null 2>&1; then
		return 0
	fi
	if [ -n "$AWS_PROFILE" ]; then
		log "No valid session for profile '$AWS_PROFILE'; running aws sso login."
		aws sso login --profile "$AWS_PROFILE"
		if ! aws "${aws_args[@]}" sts get-caller-identity >/dev/null 2>&1; then
			log "error: still no valid session for profile '$AWS_PROFILE' after sso login; check the profile and account."
			exit 1
		fi
	else
		log "error: no usable AWS credentials (set AWS_PROFILE or configure ambient credentials)."
		exit 1
	fi
}

bucket_exists() {
	aws "${aws_args[@]}" s3api head-bucket --bucket "$STATE_BUCKET" >/dev/null 2>&1
}

create_bucket() {
	# us-east-1 rejects a LocationConstraint; every other region requires it.
	if [ "$AWS_REGION" = "us-east-1" ]; then
		aws "${aws_args[@]}" s3api create-bucket --bucket "$STATE_BUCKET" >/dev/null
	else
		aws "${aws_args[@]}" s3api create-bucket \
			--bucket "$STATE_BUCKET" \
			--create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null
	fi
}

main() {
	ensure_session

	if bucket_exists; then
		log "State bucket '$STATE_BUCKET' already exists; reasserting configuration."
	else
		log "Creating state bucket '$STATE_BUCKET' in $AWS_REGION."
		create_bucket
	fi

	# Versioning is REQUIRED for native S3 state locking.
	aws "${aws_args[@]}" s3api put-bucket-versioning \
		--bucket "$STATE_BUCKET" \
		--versioning-configuration Status=Enabled

	aws "${aws_args[@]}" s3api put-bucket-encryption \
		--bucket "$STATE_BUCKET" \
		--server-side-encryption-configuration \
		'{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'

	aws "${aws_args[@]}" s3api put-public-access-block \
		--bucket "$STATE_BUCKET" \
		--public-access-block-configuration \
		'{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}'

	log "Done. State bucket '$STATE_BUCKET' is versioned, encrypted, and private."
	log "Next: cd stack/terraform/dev && terraform init && terraform apply"
}

main "$@"
