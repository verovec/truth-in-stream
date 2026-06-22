---
name: slash-commands
description: Use when authoring or modifying a slash command in .claude/commands for this workspace - file layout, frontmatter, the delegate-to-skills / dispatch-to-subagents pattern, and argument conventions
---

# Authoring Slash Commands (.claude/commands)

A command is a single Markdown file at `.claude/commands/<name>.md`. The filename IS the command:
`pick.md` is invoked as `/pick`. Frontmatter carries exactly ONE key, `description:` (one line).
Do not add `argument-hint`, `allowed-tools`, or `model` - none of the commands here use them. The
body is terse imperative prose, almost always a numbered step list.

Commands ORCHESTRATE. They hold no durable rules of their own: they delegate the rules to a skill
and push heavy or noisy work to a subagent. Keep the file thin - if a rule outlives the command,
it belongs in a skill, not here.

## Delegation - the core pattern

Two outward dependencies, used by almost every command:

1. **Delegate rules to a skill.** State it up front and tell the agent to read it first, e.g.
   `card.md`: "Use the roadmap-linear skill for structure, tone, and MCP usage. Read it first."
   `pick.md` leans on `roadmap-linear` and `delivering-linear-cards`; `research.md` on
   `research-patterns`; `reconcile.md` on `delivering-linear-cards`; `setup.md` on
   `coding-philosophy` + `research-patterns`. The skill owns the conventions; the command owns
   the sequence.
2. **Dispatch heavy/noisy work to a subagent.** Anything that burns context (web research, broad
   review, running init CLIs) runs in an isolated subagent so the command's own context stays clean.

Subagents live in `.claude/agents/`:

| Subagent | Dispatch it when | Used by |
|---|---|---|
| `researcher` | confirming latest stable version + idiomatic integration (Context7, then web) | `research.md`, `setup.md` |
| `reviewer` | reviewing a focused diff/file set for correctness and coding-philosophy adherence | (review-bearing flows) |
| `scaffolder` | running official stack-init CLIs to produce a runnable skeleton under `stack/` | `setup.md` |

## Arguments

Prefer ask-don't-assume: most commands gather inputs interactively rather than reading positional
args (`card.md`, `setup.md`, `version.md`, `roadmap.md`). Use `$ARGUMENTS` only when a real argument
is needed - `nexus.md` reads `$ARGUMENTS` for an optional `force`. There is no `$1`/`$2` positional
convention; flags like `pick`'s `--steal <ID>` are documented in an `## Arguments` section and parsed
from prose, not positionally.

## mayday is the router

`mayday.md` is a pure router - the entry menu. It carries no logic of its own: it maps the user's
intent to one of the other commands and dispatches. When unsure it asks one question, then routes.
It deliberately does NOT scan the codebase.

## Command reference

| Command | Purpose |
|---|---|
| `/mayday` | Menu and router - map intent to the right command, no logic of its own |
| `/setup` | Bootstrap a new project from this template: detach, scaffold the stack, link a GitHub repo |
| `/roadmap` | Sync the roadmap state file from Linear (card list, dependency graph, ready queue) |
| `/pick` | Claim the next ready card (parallel-safe) and deliver it in an isolated worktree |
| `/card` | Create or update a Linear card following the workspace card rules |
| `/research` | Verify current best practice and latest version before integrating a pattern |
| `/version` | Compare the local `VERSION` file with the Linear version card |
| `/reconcile` | Rebase card branches onto latest main, resolving conflicts per branch |
| `/nexus` | Update the GitNexus index for this repo and report status |

## How to author one

1. Create `.claude/commands/<name>.md`; the filename becomes `/<name>`.
2. Add frontmatter with a single `description:` line (one sentence) - nothing else.
3. Write the body as terse imperative numbered steps.
4. Delegate the durable rules to a skill ("Use the X skill ... Read it first") instead of inlining them.
5. Dispatch heavy or noisy work to a subagent (`researcher`, `reviewer`, `scaffolder`).
6. Handle inputs by asking interactively; reach for `$ARGUMENTS` only for a genuine argument.
7. Keep it thin and no emojis.

## Pitfalls

1. Do not put durable rules in a command - they belong in a skill (e.g. `roadmap-linear`,
   `delivering-linear-cards`, `research-patterns`). The command references the skill and tells the
   agent to read it first.
2. Do not add frontmatter keys beyond `description`. No `argument-hint`, `allowed-tools`, or `model`.
3. Do not invent a positional `$1`/`$2` convention. Ask for inputs, or use `$ARGUMENTS` for the one
   real argument (see `nexus.md`).
4. Do not run heavy/noisy work inline - dispatch a subagent so the command's context stays clean.
5. Do not give `mayday` logic. It routes only; new behavior goes in a dedicated command it routes to.
6. Do not let the command scan the codebase for structure - lean on the indexer/Context7 on demand.
7. The filename is the contract: rename the file to rename the command, and keep it kebab-case.
