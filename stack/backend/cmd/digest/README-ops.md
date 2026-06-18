# digest - development digest

`digest` assembles a card-centric summary of development - the cards shipped in
the window (each with a one-line description of what it delivered), the project's
remaining work, open GitHub pull requests, and stalled In Progress cards - and
posts it to Slack as a Block Kit message, or prints it to the terminal.

It runs in two modes: the **daily** digest (cards finished in the last 24 hours)
and an **epic recap** (`--epic VER-93`: one epic's finished children instead of
the window). It runs independently of the API server: a cron job (or any
scheduler) invokes the binary; there is no long-running process.

## Modes

| Command | Effect |
|---------|--------|
| `go run ./cmd/digest` | Post the daily Block Kit digest to Slack (default). |
| `go run ./cmd/digest --epic VER-93` | Recap a finished epic instead of the daily window. |
| `go run ./cmd/digest --terminal` | Print the full, untruncated report to stdout. |
| `go run ./cmd/digest --dry-run` | Print the Slack Block Kit JSON without posting. |
| `make digest` | Same as the default (post to Slack). |
| `make digest EPIC=VER-93` | Post the epic recap (combine with `MODE=terminal`/`dry-run`). |

The epic recap is posted automatically when an epic's last card is delivered -
see the `delivering-linear-cards` skill, which checks the epic's siblings on
merge and runs `make digest EPIC=<parent>` when they are all Done.

## Configuration (environment only)

| Variable | Required | Purpose |
|----------|----------|---------|
| `SLACK_DIGEST_WEBHOOK_URL` | Yes, for Slack mode | Slack incoming-webhook URL. Absent -> the binary exits with a clear error (use `--terminal`/`--dry-run` without it). Provision it as a secret; never commit it. |
| `LINEAR_API_KEY` | No | Enables the shipped, remaining, and blocker sections (and `--epic`). Absent -> those sections show a note. |
| `LINEAR_PROJECT` | No | Linear project name (default `Truth in Stream`). |
| `GITHUB_TOKEN` | No | Raises the GitHub rate limit. The public repo works without it. |
| `GITHUB_REPO` | No | `owner/name` (default `verovec/truth-in-stream`). |
| `DIGEST_SUMMARY_API_KEY` | No | Anthropic key for the per-card "what was implemented" descriptions. Absent -> shipped cards fall back to their titles. |
| `DIGEST_SUMMARY_MODEL` | No | Summary model (default `claude-haiku-4-5-20251001`). |

A missing source never fails the digest: it degrades to a note in the report so
the remaining sections still render. All outbound HTTP calls use a 10-second
timeout.

## Scheduling

Run once each morning. Example crontab entry (08:00 Europe/Paris):

```cron
CRON_TZ=Europe/Paris
0 8 * * * cd /path/to/repo/stack/backend && SLACK_DIGEST_WEBHOOK_URL=… LINEAR_API_KEY=… GITHUB_TOKEN=… go run ./cmd/digest >> /var/log/digest.log 2>&1
```

In production, supply the secrets from the environment / secret store rather than
inline (e.g. an `EnvironmentFile` for a systemd timer, or the ECS task secrets if
the digest is run as a scheduled Fargate task). Build a static binary with
`CGO_ENABLED=0 go build ./cmd/digest` and schedule that instead of `go run` off a
host with the toolchain.

## On-demand report

The `/report` skill (`.claude/skills/report`) runs the terminal mode and prints
the full report - no Slack webhook required.
