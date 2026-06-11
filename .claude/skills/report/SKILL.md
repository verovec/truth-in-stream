---
name: report
description: Print the detailed daily development digest (recent commits, Linear card activity, open PRs, and stalled In Progress cards) to the terminal. Use when the user asks for a status report, a daily digest, a standup summary, or runs /report.
---

# /report

Run the digest in terminal mode from the backend module and show the user the full output:

```bash
cd stack/backend && go run ./cmd/digest --terminal
```

Print the command's output to the user verbatim. The report covers the last 24 hours:

- **Commits** - git commits with author and files changed.
- **Linear activity** - cards updated in the window with their current state.
- **Open pull requests** - open PRs on the repository.
- **Blockers** - In Progress cards whose branch saw no commit in the window.

The terminal report is untruncated (unlike the Slack digest, which caps long sections).

Sections that need credentials degrade to a note when the credential is unset: the Linear
sections need `LINEAR_API_KEY`; GitHub uses `GITHUB_TOKEN` if present (the public repo also
works without it). The git section always works. Do not pass or print any Slack webhook URL -
the terminal mode never needs it.
