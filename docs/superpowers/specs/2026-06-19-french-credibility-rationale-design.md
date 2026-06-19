# French rationale for the credibility verifier

## Problem

The retrieve-then-verify fact-check path shows the viewer a one-sentence
`rationale` explaining each verdict. When `FACTCHECK_POLITICAL` is off, claims
run through the **credibility verifier** (`internal/verify/verify.go`), whose
system prompt is in English, so the rationale comes back in English even though
the rest of the UI (status labels, evidence-source labels) is French. The
political verifier (`internal/verify/political.go`) already emits a French
rationale ("Redige le rationale en francais"); the credibility verifier does
not.

A live example: a French claim about the 1958 Constitution returns the French
status "Conteste" and source "Wikipedia" but an English explanation paragraph.

## Goal

The credibility verifier's `rationale` is in French when the service runs in
French political mode, and unchanged (English) otherwise.

## Scope

In scope: `internal/verify/verify.go` only - the single viewer-facing LLM output
still in English.

Out of scope: every other LLM module. `political.go`, `claimtype.go`,
`claimdecomp.go`, and `checkworthy.go` already run in French under the French
locale; `stance.go` and `evidencegate.go` emit internal signals (`reason`) never
shown to the viewer; `digestsummary.go` feeds the internal dev digest, not the
viewer. None of these are touched.

## Approach

Locale-aware prompt, identical to the established pattern in `checkworthy.go`
and `claimdecomp.go`: an English `systemPrompt`, a French `systemPromptFR`, and a
`promptFor(locale)` selector keyed on `domain.Locale.IsFrench()`. French is
driven entirely by the system prompt; the forced-tool JSON schema stays English
because it is model instruction, not output - same as `political.go`.

### Changes

1. **`internal/verify/verify.go`**
   - Keep the existing English `systemPrompt`.
   - Add `systemPromptFR`: a faithful French translation of `systemPrompt` that
     ends with an explicit instruction to write the rationale in French in one
     sentence, mirroring `political.go`.
   - Add `func promptFor(locale domain.Locale) string` returning `systemPromptFR`
     for `locale.IsFrench()`, `systemPrompt` otherwise.
   - Add `Locale domain.Locale` to `Config`.
   - Add a `system string` field to `Client`; set it in `New` via
     `promptFor(cfg.Locale)`; `Verify` reads `c.system` instead of the package
     const. (Same shape as `checkworthy.Client`.)

2. **`cmd/server/main.go`** - in `buildVerifyPath`, add `Locale: locale` to the
   credibility `verify.Config{...}` (line ~467). `locale` is already a parameter
   of `buildVerifyPath`. The political `verify.Config` (line ~515) is left alone:
   the political verifier is hardcoded French via `VerifyPolitical`.

3. **`internal/verify/verify_test.go`** - a table-driven test that captures the
   request the client sends to a fake server and asserts the French locale yields
   the French system prompt while the default locale keeps the English one.
   Mirror `checkworthy`'s locale test.

### Backward compatibility

`Locale` is a new field whose zero value resolves to the English prompt. So
`internal/eval` (which constructs `verify.Config` without a locale) and the
political verifier are behaviorally unchanged.

## Testing

- Unit (table-driven, `-race`): French locale yields the French system prompt;
  default locale keeps the English system prompt; the rest of the request (tool
  name, forced call) is identical across locales.
- Existing `verify_test.go` cases (citation guard, verdict decoding) continue to
  pass unchanged.

## Definition of Done

Acceptance met; `go test -race ./...`, `go vet`, `golangci-lint`, `gofumpt` green;
code review passed; PR rebased on `main`, CI green, merged.
