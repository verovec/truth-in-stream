---
name: report
description: Print the detailed development digest (cards shipped in the window, the project's remaining work, open PRs, and stalled In Progress cards) to the terminal. Use when the user asks for a status report, a daily digest, a standup summary, or runs /report.
---

# /report

Run the digest in terminal mode from the backend module and show the user the full output:

```bash
cd stack/backend && go run ./cmd/digest --terminal
```

To recap a finished epic instead of the daily window, pass `--epic`:

```bash
cd stack/backend && go run ./cmd/digest --terminal --epic VER-93
```

Print the command's output to the user verbatim. The report covers the last 24 hours
(daily mode) or one epic's children (epic mode):

- **Shipped** - cards finished in the window (daily) or the epic's finished children (epic
  mode), each with a one-line description of what it delivered.
- **Remaining** - the project's not-Done cards, grouped by state.
- **Open pull requests** - open PRs on the repository.
- **Blockers** - In Progress cards whose branch saw no commit in the window.

The terminal report is untruncated (unlike the Slack digest, which caps long sections).

Sections that need credentials degrade gracefully when the credential is unset: the
shipped/remaining/blocker sections need `LINEAR_API_KEY`; GitHub uses `GITHUB_TOKEN` if present
(the public repo also works without it); the per-card descriptions need `DIGEST_SUMMARY_API_KEY`
and fall back to the card titles without it. Do not pass or print any Slack webhook URL - the
terminal mode never needs it.
