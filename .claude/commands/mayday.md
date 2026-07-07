---
description: Menu and router for this workspace's commands
---

Route the user to the right command:

- New project from this template -> run `/setup`.
- Sync the roadmap from Linear (card list, dependency graph, ready queue) -> run `/roadmap`.
- Pick up and deliver the next ready card (parallel-safe, one card per session) -> run `/pick`.
- Create or update a Linear card -> run `/card`.
- Verify best practice / latest version for a pattern -> run `/research`.
- Compare local vs Linear version -> run `/version`.
- Rebase card branches onto latest main and resolve conflicts -> run `/reconcile`.
- Run a source's cloud producer on the crawler ingestion host to fill its queue -> run `/crawler`.
- Bring a source's cloud worker up on the consumer ingestion host to drain its queue -> run `/consumer`.

If the user is unsure, ask one question to identify intent, then route. Do not scan the
codebase here - the Understand-Anything plugin and Context7 provide structure on demand.
