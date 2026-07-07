# Public-repo hygiene + ingest-guard hardening - design

Date: 2026-07-08
Status: implemented in this change; tracked by the security card

## Problem

`github.com/verovec/truth-in-stream` is a **public** repository, and real AWS account ids were
committed across it: the app account and the main/DNS account appeared in `deploy/targets.json`
(the `/ingest`, `/consumer`, `/crawler` account guard's source of truth), in three guard/host test
scripts as the default live account, in a Terraform variable default, and in Terraform docs and
comments. Account ids are not credentials, but on a public repo they aid targeted attacks,
resource enumeration, and cross-account trust probing. The prior design deliberately committed them
("identifiers, not secrets, so committing is correct and makes the guard trustworthy"); this change
overturns that decision.

## Decision

Chosen scope: **externalize the account ids going forward, keep git history, and add an automated
scan.** Rationale:

- The guard does **not** need the expected ids committed. Its job is to stop an operator targeting
  the wrong account; the operator trusts their own machine, so a **gitignored local file** is an
  equally trustworthy source of truth and preserves the guard's non-circular property (the expected
  id is still resolved independently of the account being targeted).
- History is **not** rewritten: the ids are non-secret identifiers already public in history;
  purging them needs a force-push to `main` (not permitted autonomously) and still leaves cached and
  forked copies. Stopping new exposure and preventing recurrence is the high-value, low-risk move.
- The `/ingest`, `/consumer`, `/crawler` tooling is **kept** - the scripts are not the leak; the
  committed ids were. Deleting the tooling would remove useful operator capability for no benefit.

## What changed

- `deploy/targets.json` is **gitignored** and untracked (the local copy is retained). The committed
  template is `deploy/targets.example.json` (placeholders only). The guard's not-found message tells
  the operator to copy the example and fill real ids locally.
- `*.tfvars` is **gitignored** by default (the committed template is `*.tfvars.example`; the only
  tracked `.tfvars` are the dev/prod non-sensitive selectors - `aws_region` + `environment` - kept
  via a `.gitignore` negation so CI auto-loads them, and the scan still covers them). The
  `main_account_id`
  Terraform default is a placeholder (`000000000000`) that fails the `allowed_account_ids` guard
  closed; the real id is supplied via a gitignored `terraform.tfvars` (documented in the example).
- The three test scripts use an obviously-fake fixture id instead of the real account id.
- Terraform docs and comments use descriptive placeholders (`<main-account-id>`, `<app-account-id>`).
- The guard script's "committing is correct" rationale is inverted to "gitignored local source of
  truth; committed template is the example".
- A new **`public-repo-hygiene` skill** states the policy (never commit secrets or infra
  identifiers; where real values live; the placeholder + `.example` pattern; the pre-commit scan)
  and is registered in `CLAUDE.md` (always-on rule + on-demand list).
- A new **`scripts/secret-scan.sh`** fails on any unrecognized 12-digit account-shaped token in a
  tracked config/doc/script file (allow-listing placeholders/fixtures; Go source and the embedding
  seed cache are out of scope by construction). It is wired into `.github/workflows/pr.yml` as two
  jobs (unit test of the scanner + a scan of the real tree) and available as `make secret-scan`.
  An opt-in pre-commit hook (`.githooks/pre-commit`, enabled by `make install-hooks`) runs the same
  scan locally.

## Verification

- `scripts/secret-scan.test.sh`: 8/8 (leak fails; placeholders/fixtures/out-of-scope/gitignored
  pass), including the scanner being clean when its own test file is tracked (leak tokens assembled
  at runtime, never a literal 12-digit run in the source).
- `scripts/secret-scan.sh` on the real tree: clean.
- `aws-target-guard.test.sh` (24), `ingest-run.test.sh` (73), `ingest-host.test.sh` (81): green
  after the fixture scrub.
- `terraform fmt -check -recursive stack/terraform/main-account`: clean.

## Follow-ups (not in this change)

- Other infrastructure identifiers (hosted-zone id, VPC/subnet/SG ids, internal DNS) are lower
  sensitivity and some are operationally documented; a later sweep can externalize them per the
  skill's policy if desired. The account-id scan does not enforce them.
- Making the CI `account-id-scan` a **required** status check (branch protection) so a re-leak
  actually blocks the auto-merge, not just reports - a GitHub settings change the maintainer owns.
