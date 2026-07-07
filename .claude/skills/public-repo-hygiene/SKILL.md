---
name: public-repo-hygiene
description: Use before committing, pushing, opening a PR, or writing any config, script, Terraform, docs, or slash-command content that could carry infrastructure identifiers or secrets. This repository is PUBLIC on GitHub; everything committed is world-readable forever. Covers what must never be committed (credentials AND AWS account ids/ARNs/internal endpoints), where real values live instead (gitignored files, env, SSM/Secrets Manager), the placeholder + .example pattern, and the pre-commit scan reflex.
---

# Public-Repo Hygiene

`github.com/verovec/truth-in-stream` is a **PUBLIC** repository. Every commit is world-readable
the instant it is pushed, and it stays readable forever: history rewrites do not reach existing
clones, forks, or GitHub's caches. There is no "delete it later". The only safe assumption is
that anything you commit is already public. Treat every commit as a publish action.

Read this before you commit, push, open a PR, or write any config, script, Terraform, docs, or
slash-command content.

## NEVER commit

- **Credentials of any kind** - API keys, tokens, passwords, private keys, session secrets,
  connection strings with embedded passwords, `.pem`/`.p12` files. This is absolute.
- **AWS account ids** (the 12-digit numbers) and **account-bearing ARNs**
  (`arn:aws:iam::<account-id>:...`). Account ids are not credentials, but on a public repo this
  project treats them as not-for-publication: a public account id aids targeted attacks, resource
  enumeration, and cross-account trust probing, and removes the account's anonymity. Defense in
  depth - keep them off the repo.
- **Internal hostnames, private endpoints, and infrastructure resource ids** that only exist to
  serve the running system (hosted-zone ids, VPC/subnet/security-group ids, internal DNS names,
  bastion addresses) when they are not already required to be public.

If you are unsure whether a value is sensitive, treat it as sensitive and use a placeholder.

## Where real values live instead

Real values live **outside version control**, and the committed tree carries only placeholders:

- **Gitignored local files** for per-operator/per-environment truth:
  - `deploy/targets.json` (the `/ingest`, `/consumer`, `/crawler` account guard's expected account
    ids) - gitignored; the committed template is `deploy/targets.example.json` with `000000000000`
    placeholders. An operator copies the example and fills real ids locally.
  - `*.tfvars` (Terraform variable values, including account ids) - gitignored by default; the
    committed template is `*.tfvars.example`. The only tracked `.tfvars` are the dev/prod
    non-sensitive selectors (`aws_region` + `environment` only), kept via a `.gitignore` negation
    so CI can auto-load them; the account-id scan still covers them.
  - `.env` (local dev secrets) - gitignored; the committed template is `.env.example`.
- **Environment variables** read by the code/config at run time.
- **AWS SSM Parameter Store / Secrets Manager** for values the running infrastructure needs.
  Workers read their broker URL, DSN, and embedding key from Secrets Manager on the host - never
  from the repo.

The rule: **a committed file names a value with a placeholder; the real value is supplied at run
time from a gitignored file, an env var, or a secrets store.** A committed `.example` documents
the shape without carrying the secret.

## The placeholder convention

- AWS account id -> `000000000000` (or an obviously-fake `999999999999` in a test fixture).
- Test fixtures use obviously-fake ids (`111111111111`, `222222222222`, `123456789012`); never the
  real account id, even as a "default".
- ARNs -> `arn:aws:iam::000000000000:role/<name>` in examples and docs.

## Pre-commit reflex (MUST run before every commit)

Before you `git add`/`git commit`, scan your staged change for leaked identifiers:

```
scripts/secret-scan.sh           # scans tracked files; exits non-zero on a real account id
```

or, for a quick manual check of what you are about to commit:

```
git diff --cached | grep -nE '[0-9]{12}|arn:aws:[a-z0-9-]*:[a-z0-9-]*:[0-9]{12}'
```

If it matches anything that is not an allow-listed placeholder or an obviously-fake fixture id,
**stop and externalize it** before committing. The known real ids to never introduce are the app
account (the ECS/RDS account) and the main/DNS account; both must appear only as placeholders in
tracked files.

## Enforcement (this is a gate, not a guideline)

- **CI** runs `scripts/secret-scan.sh` on every pull request (a job in `.github/workflows/pr.yml`).
  A PR that reintroduces a real account id into a tracked file fails the check. Do not add the real
  id to the scan's allow-list to get a green check - fix the leak.
- **Local pre-commit hook** (opt-in): run `make install-hooks` once to point `core.hooksPath` at
  `.githooks/`, so the same scan runs before every local commit.

## When you find a leak already committed

- A real value already in history is **compromised as if published** - rotate/revoke it if it is a
  credential (account ids cannot be "rotated"; they are identifiers).
- Removing a value going forward (gitignore + placeholder) stops new exposure but does **not** purge
  history. A history rewrite requires a force-push to the affected branch (never `main` without
  explicit human coordination) and still leaves cached/forked copies. Report the finding; do not
  rewrite shared history on your own.

## Cross-references

- The account guard that consumes `deploy/targets.json`: `scripts/aws-target-guard.sh` (used by
  `/ingest`, `/consumer`, `/crawler`). Its expected account ids are the local source of truth and
  are deliberately **not** committed.
- Terraform variable values (including `main_account_id`) come from a gitignored `*.tfvars`; the
  committed roots carry placeholder defaults and a `*.tfvars.example` template.
