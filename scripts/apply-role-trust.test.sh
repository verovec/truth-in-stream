#!/usr/bin/env bash
# Unit test for apply-role-trust.sh. Stubs the aws CLI on PATH and asserts the
# trust policy written to the CI apply role: the exact least-privilege OIDC
# subjects, the aud condition, the account-derived provider ARN, and the
# fail-fast on a missing role name. No AWS account or credentials are involved.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TRUST="$SCRIPT_DIR/apply-role-trust.sh"

STUB_ACCOUNT_ID="123456789012"

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

assert_contains() {
	# assert_contains <file> <substring> <message>
	grep -qF -- "$2" "$1" || fail "$3 (expected to find: $2)"
}

assert_not_contains() {
	grep -qF -- "$2" "$1" && fail "$3 (did not expect to find: $2)" || true
}

# run_trust <log_path> [extra env assignments...]
# Builds a fake `aws` on a temp PATH that records every call to <log_path>.
# Returns the script's exit code.
run_trust() {
	local log="$1"
	shift

	local bindir
	bindir="$(mktemp -d)"
	cat >"$bindir/aws" <<FAKE
#!/usr/bin/env bash
# Record the full invocation, then simulate just enough of the aws CLI.
printf '%s\n' "\$*" >>"$log"
# Find the api subcommand (skip leading --profile/--region flags).
sub=""
prev=""
for a in "\$@"; do
	case "\$prev" in
		--profile|--region) prev="\$a"; continue ;;
	esac
	case "\$a" in
		--*) prev="\$a"; continue ;;
	esac
	sub="\$a"; break
done
case "\$sub" in
	sts)
		# get-caller-identity; honour --query Account by printing the id.
		case "\$*" in
			*"--query Account"*) printf '%s\n' "$STUB_ACCOUNT_ID" ;;
		esac
		exit 0
		;;
	*) exit 0 ;;
esac
FAKE
	chmod +x "$bindir/aws"

	: >"$log"
	local rc=0
	# Hermetic: unset the ambient variables the script defaults (CI always sets
	# GITHUB_REPOSITORY; a developer may export AWS_PROFILE or APPLY_ROLE_NAME),
	# so the cases below test the script's own defaults, then apply overrides.
	env -u GITHUB_REPOSITORY -u AWS_PROFILE -u APPLY_ROLE_NAME "$@" \
		PATH="$bindir:$PATH" bash "$TRUST" >/dev/null 2>&1 || rc=$?
	rm -rf "$bindir"
	return "$rc"
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Case 1: happy path -> the trust policy is written to the given role with the
# three literal CI subjects, the aud condition, and the provider ARN derived
# from the caller's account.
run_trust "$tmp/happy.log" APPLY_ROLE_NAME=test-apply-role || fail "happy path must exit 0"
assert_contains "$tmp/happy.log" "iam update-assume-role-policy --role-name test-apply-role" "trust policy must target the given role"
assert_contains "$tmp/happy.log" "arn:aws:iam::${STUB_ACCOUNT_ID}:oidc-provider/token.actions.githubusercontent.com" "provider ARN must be derived from the caller account"
assert_contains "$tmp/happy.log" '"sts.amazonaws.com"' "aud must be pinned to sts.amazonaws.com"
assert_contains "$tmp/happy.log" '"repo:verovec/truth-in-stream:ref:refs/heads/main"' "main-merge subject must be trusted"
assert_contains "$tmp/happy.log" '"repo:verovec/truth-in-stream:environment:production"' "release production-environment subject must be trusted"
assert_not_contains "$tmp/happy.log" "pull_request" "ungated PR code must never assume the prod-writing apply role"
assert_contains "$tmp/happy.log" "StringEquals" "subjects are literal, matched with StringEquals"
assert_not_contains "$tmp/happy.log" "StringLike" "no glob matching in the apply-role trust"
assert_not_contains "$tmp/happy.log" ':*"' "no wildcard subject may be trusted"
assert_contains "$tmp/happy.log" "--profile truth-in-stream-dev" "default SSO profile must be used"

# Case 2: missing APPLY_ROLE_NAME -> fail fast, never touch IAM.
if run_trust "$tmp/norole.log"; then
	fail "missing APPLY_ROLE_NAME must fail"
fi
assert_not_contains "$tmp/norole.log" "update-assume-role-policy" "no trust update without a role name"

# Case 3: empty AWS_PROFILE -> no --profile flag (ambient credentials, e.g. CI).
run_trust "$tmp/noprofile.log" APPLY_ROLE_NAME=test-apply-role AWS_PROFILE= || fail "ambient-credential run must exit 0"
assert_not_contains "$tmp/noprofile.log" "--profile" "empty AWS_PROFILE must omit the profile flag"
assert_contains "$tmp/noprofile.log" "update-assume-role-policy" "trust update still runs without a profile"

# Case 4: custom GITHUB_REPOSITORY -> the subjects follow it.
run_trust "$tmp/repo.log" APPLY_ROLE_NAME=test-apply-role GITHUB_REPOSITORY=octo/example || fail "custom-repo run must exit 0"
assert_contains "$tmp/repo.log" '"repo:octo/example:environment:production"' "subjects must follow GITHUB_REPOSITORY"
assert_not_contains "$tmp/repo.log" "verovec/truth-in-stream" "default repo must not leak into a custom-repo trust"

printf 'ok - apply-role-trust.sh: all assertions passed\n'
