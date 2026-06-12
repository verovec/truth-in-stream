#!/usr/bin/env bash
# Unit test for bootstrap-tfstate.sh. Stubs the aws CLI on PATH and asserts the
# orchestration: the bucket is created only when missing, and versioning,
# encryption, and a full public-access block are asserted on every run. No AWS
# account or credentials are involved.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP="$SCRIPT_DIR/bootstrap-tfstate.sh"

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

# run_bootstrap <bucket_exists 0|1> <log_path> [extra env assignments...]
# Builds a fake `aws` on a temp PATH that records every call to <log_path>.
run_bootstrap() {
	local bucket_exists="$1" log="$2"
	shift 2

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
	sts) exit 0 ;;                       # get-caller-identity: always valid
	s3api)
		# The api action is the token after s3api.
		seen_s3api=0
		for a in "\$@"; do
			if [ "\$seen_s3api" = 1 ]; then action="\$a"; break; fi
			[ "\$a" = "s3api" ] && seen_s3api=1
		done
		if [ "\$action" = "head-bucket" ]; then
			[ "$bucket_exists" = "1" ] && exit 0 || exit 1
		fi
		exit 0
		;;
	*) exit 0 ;;
esac
FAKE
	chmod +x "$bindir/aws"

	: >"$log"
	env "$@" PATH="$bindir:$PATH" bash "$BOOTSTRAP" >/dev/null 2>&1
	rm -rf "$bindir"
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Case 1: bucket missing -> it is created with the region LocationConstraint, and
# all hardening is applied.
run_bootstrap 0 "$tmp/missing.log"
assert_contains "$tmp/missing.log" "s3api create-bucket --bucket truth-in-stream-tfstate" "missing bucket should be created"
assert_contains "$tmp/missing.log" "LocationConstraint=eu-west-3" "create should pin the region"
assert_contains "$tmp/missing.log" "put-bucket-versioning --bucket truth-in-stream-tfstate --versioning-configuration Status=Enabled" "versioning must be enabled"
assert_contains "$tmp/missing.log" "SSEAlgorithm" "default encryption must be set"
assert_contains "$tmp/missing.log" '"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true' "public access must be fully blocked"
assert_contains "$tmp/missing.log" "--profile truth-in-stream-dev" "default SSO profile must be used"
assert_contains "$tmp/missing.log" "--region eu-west-3" "region flag must be passed"

# Case 2: bucket already exists -> it is NOT recreated, but hardening is still
# reasserted (idempotency).
run_bootstrap 1 "$tmp/exists.log"
assert_not_contains "$tmp/exists.log" "create-bucket" "existing bucket must not be recreated"
assert_contains "$tmp/exists.log" "put-bucket-versioning" "versioning is reasserted on an existing bucket"
assert_contains "$tmp/exists.log" "put-public-access-block" "public-access block is reasserted on an existing bucket"

# Case 3: empty AWS_PROFILE -> no --profile flag (ambient credentials, e.g. CI).
run_bootstrap 0 "$tmp/noprofile.log" AWS_PROFILE=
assert_not_contains "$tmp/noprofile.log" "--profile" "empty AWS_PROFILE must omit the profile flag"
assert_contains "$tmp/noprofile.log" "create-bucket" "bootstrap still runs without a profile"

# Case 4: custom region -> LocationConstraint follows AWS_REGION.
run_bootstrap 0 "$tmp/region.log" AWS_REGION=eu-central-1
assert_contains "$tmp/region.log" "LocationConstraint=eu-central-1" "create must honour AWS_REGION"

printf 'ok - bootstrap-tfstate.sh: all assertions passed\n'
