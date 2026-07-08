#!/usr/bin/env bash
# Set the CI apply role's GitHub-OIDC trust policy to the exact subjects the
# terraform workflows authenticate with. The apply role (the AWS_ROLE_ARN
# repository secret) is bootstrapped out of band with elevated credentials — it
# cannot be managed by the terraform it runs without a chicken-and-egg — so its
# trust policy is defined here, next to the other bootstrap procedure
# (bootstrap-tfstate.sh), and this script is the single place it is written.
#
# Idempotent: update-assume-role-policy replaces the whole document, so
# re-running always converges on the same trust. Run it once when wiring
# AWS_ROLE_ARN, and again whenever a workflow's OIDC subject changes.
#
# Trusted subjects (least-privilege: literal values, matched with StringEquals,
# never a wildcard):
#   repo:<repo>:ref:refs/heads/main      terraform.yml plan + dev apply on merge
#                                        to main
#   repo:<repo>:environment:production   release.yml prod apply; the job binds
#                                        the `production` GitHub Environment, so
#                                        GitHub issues the environment subject,
#                                        not the tag ref
#
# The ungated pull_request subject is deliberately NOT trusted: a PR can edit
# its own pull_request-triggered workflows, so trusting it would let any
# same-repo PR assume this prod-writing role and apply without the `production`
# Environment approval. PR terraform runs validate offline instead
# (_terraform.yml); the pre-apply IAM guard still runs on every credentialed
# plan (merge to main, release).
#
# Env overrides:
#   APPLY_ROLE_NAME    IAM role name of the CI apply role (required)
#   GITHUB_REPOSITORY  org/repo the trust is scoped to
#                      (default verovec/truth-in-stream)
#   AWS_REGION         (default eu-west-3; IAM is global, kept for a consistent
#                      CLI target)
#   AWS_PROFILE        AWS SSO profile name (default truth-in-stream-dev; unset
#                      to use ambient credentials)
set -euo pipefail

APPLY_ROLE_NAME="${APPLY_ROLE_NAME:-}"
GITHUB_REPOSITORY="${GITHUB_REPOSITORY:-verovec/truth-in-stream}"
AWS_REGION="${AWS_REGION:-eu-west-3}"
# Default to the documented dev SSO profile, but allow an explicit empty value
# (AWS_PROFILE=) to fall back to ambient credentials.
AWS_PROFILE="${AWS_PROFILE-truth-in-stream-dev}"

log() { printf '%s\n' "$*" >&2; }

if [ -z "$APPLY_ROLE_NAME" ]; then
	log "error: APPLY_ROLE_NAME is required (the IAM role name behind the AWS_ROLE_ARN repository secret)."
	exit 1
fi

# aws_args holds the shared --profile/--region flags so every call targets the
# same account. --profile is omitted when AWS_PROFILE is empty.
aws_args=(--region "$AWS_REGION")
if [ -n "$AWS_PROFILE" ]; then
	aws_args+=(--profile "$AWS_PROFILE")
fi

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

main() {
	ensure_session

	local account_id provider_arn policy
	account_id="$(aws "${aws_args[@]}" sts get-caller-identity --query Account --output text)"
	if [ -z "$account_id" ]; then
		log "error: could not resolve the caller account id."
		exit 1
	fi
	# The GitHub OIDC provider is account-global, created once by the dev
	# environment (modules/iam, create_oidc_provider = true).
	provider_arn="arn:aws:iam::${account_id}:oidc-provider/token.actions.githubusercontent.com"

	policy="$(cat <<JSON
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "GitHubOidcTerraformCi",
      "Effect": "Allow",
      "Principal": { "Federated": "${provider_arn}" },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          "token.actions.githubusercontent.com:sub": [
            "repo:${GITHUB_REPOSITORY}:ref:refs/heads/main",
            "repo:${GITHUB_REPOSITORY}:environment:production"
          ]
        }
      }
    }
  ]
}
JSON
)"

	log "Setting the OIDC trust policy on role '$APPLY_ROLE_NAME' (account $account_id)."
	aws "${aws_args[@]}" iam update-assume-role-policy \
		--role-name "$APPLY_ROLE_NAME" \
		--policy-document "$policy"

	log "Done. '$APPLY_ROLE_NAME' now trusts exactly:"
	log "  repo:${GITHUB_REPOSITORY}:ref:refs/heads/main"
	log "  repo:${GITHUB_REPOSITORY}:environment:production"
}

main "$@"
