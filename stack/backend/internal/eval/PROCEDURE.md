# French political golden eval — real-model run and `FACTCHECK_POLITICAL` flip

This package holds the offline golden-eval gate for the French political two-axis
fact-check path (`internal/verify.VerifyPolitical`). The gate is deterministic and
runs in CI with no external API and no database; it guards the wiring and the
citation/flag logic, not live model quality. This document records how to run the
*real-model* eval and the decision rules for two distinct calls:

- flipping the `FACTCHECK_POLITICAL` production default (does the live path hit the
  recorded accuracy floor at all?), and
- the **Gemini-vs-Haiku provider comparison** (is Gemini good enough to run a given
  stage instead of the shipped Anthropic/Haiku default?).

Both are operator-run, cost credit, and are decided on real-model numbers, never on
the faked CI gate.

## What the offline gate proves (and does not)

`TestGoldenEvalAccuracyGate` runs every case in `testdata/golden.json` through the
real `verify.Client` **under each supported provider** (DeepSeek - the default -
plus Anthropic and Gemini), against a per-provider fake server that replays each
case's recorded `record_assessment` tool-call in that provider's wire format. The
harness takes an `eval.Target` (`Provider` + optional `Model`); the gate runs the
same golden set once per target, so provider selection is exercised offline with no
network call.
`TestGoldenGateFailsOnInjectedRegression` corrupts one recorded verdict and asserts
the measured accuracy drops below the baseline under every provider, proving the
gate has teeth on each backend. It asserts two recorded baselines:

- `baselineLiteralAccuracy` — the fraction of cases whose literal verdict
  (`accurate | inaccurate | unverifiable`) matches the label.
- `baselineFlagAccuracy` — the fraction of cases whose surviving manipulation-flag
  set exactly equals the expected flag set.

Because the model output is recorded, the gate is a **regression guard on the
two-axis wiring and the deterministic citation/flag guard**, not a measurement of
how well a live model labels French political speech — and because the *recorded*
verdict is replayed identically through both providers' wire formats, the faked
gate scores 100/100 under either backend and **cannot** stand in for a real
Gemini-vs-Haiku quality comparison. A change that breaks verdict mapping, citation
grounding, or flag sanitization drops the measured accuracy below the recorded
baseline and fails the gate. The current recorded baseline is **100% literal / 100%
flag** over 38 cases, because every recorded verdict is authored to the label and
every citation span is an exact substring of its passage (so the guard keeps it).

The offline gate alone is **not** sufficient evidence to flip the production
default or to switch a stage's provider: it does not exercise live
`voyage-4-large` retrieval or a live model (Claude or Gemini) on a real corpus.

## Real-model eval procedure

Run this before proposing a flip of `FACTCHECK_POLITICAL`. It needs a throwaway
database and live API keys.

1. **Seed a throwaway Postgres + pgvector DB** with the curated claim DB and the
   political evidence stores (`make seed`). **Never** point `TEST_DATABASE_URL` or
   `DATABASE_URL` at the shared `truthinstream` dev DB — the store integration
   tests drop all tables, and the eval may write. Use a disposable database and
   restore the dev DB with `make seed` afterwards if you touched it.

2. **Configure live providers** (env only, never logged or committed):
   - `EMBEDDING_API_KEY` for live `voyage-4-large` retrieval,
   - the political verifier's Anthropic key (the key `LoadPolitical` /
     `verify.New` consume),
   - `FACTCHECK_POLITICAL=true` plus any routing knobs
     (`FACTCHECK_POLITICAL_ROUTER_MIN_RESULTS`).

3. **Run each golden statement through the live political path**: live
   classify -> route + retrieve -> `VerifyPolitical`, comparing the emitted
   `literal` verdict and `flags` against each case's `expected_literal` and
   `expected_flags` in `golden.json`. Score literal accuracy and flag accuracy the
   same way `RunPolitical` does (literal match; exact flag-set match).

4. **Record the live numbers** (literal accuracy, flag accuracy, per-label
   breakdown) in the PR that proposes the flip. The bar is: live literal accuracy
   **>= `baselineLiteralAccuracy`** AND live flag accuracy **>=
   `baselineFlagAccuracy`** on the real corpus. The faked-model baseline is the
   floor the live run must meet or beat.

## Gemini-vs-Haiku real-model comparison

The eval harness is provider-parametrized: the same golden set is scored under
whichever provider an `eval.Target` selects, so Gemini and the shipped
Anthropic/Haiku default are measured on the *same cases, same scoring* — an
apples-to-apples comparison. The faked CI gate proves the wiring is identical on
both backends; this procedure measures **live quality** and decides whether Gemini
is good enough to run a stage. It costs credit on both providers and is operator-run.

### Env to set

Provider selection is the env already added by VER-109; this card adds no new env.

- `LLM_PROVIDER` — `deepseek` (default, DeepSeek's cheap chat model), `anthropic`
  (Claude Haiku), or `gemini`. It is the single shared provider choice every
  LLM-backed stage reads.
- `DEEPSEEK_API_KEY` — required when `LLM_PROVIDER` is `deepseek` (or unset);
  `GEMINI_API_KEY` — required only when `LLM_PROVIDER=gemini`. The Anthropic key the
  stage already consumes (e.g. the political verifier's key) stays the Anthropic-side
  secret. Keys come from the environment only and are never logged or committed.
- Optionally a per-stage model override; an empty model falls back to the provider's
  default (`deepseek-v4-flash` for DeepSeek, `claude-haiku-4-5-20251001` for
  Anthropic, `gemini-2.5-flash` for Gemini).

### How to run it

Run the live political path twice over `golden.json`, once per provider, holding
everything else fixed (same retrieval, same corpus, same golden statements):

1. **Haiku baseline:** `LLM_PROVIDER=anthropic` (or unset) with the Anthropic key.
   Run each golden statement through the live path
   (classify -> route + retrieve -> `VerifyPolitical`) exactly as the
   `FACTCHECK_POLITICAL` real-model procedure above describes, scoring literal and
   flag accuracy with `RunPolitical`'s rule (literal match; exact flag-set match).
2. **Gemini candidate:** rerun the identical sweep with `LLM_PROVIDER=gemini` and
   `GEMINI_API_KEY` set, **nothing else changed**.
3. Record both runs' `Report` numbers (use `Report.Format` for a stable, diffable
   line): overall literal accuracy, overall flag accuracy, and the per-literal-label
   breakdown, for each provider.

### How to read the numbers

The two axes are independent and a provider can pass one and fail the other:

- **Literal-axis accuracy** — did the provider get the face-value verdict
  (`accurate | inaccurate | unverifiable`) right? Read the per-label breakdown, not
  just the headline: a provider that scores well overall but collapses one label
  (e.g. calls every `unverifiable` claim `inaccurate`) is not interchangeable.
- **Flag-axis accuracy** — did the surviving manipulation-flag set exactly match the
  expected set? This is the redesign's hard part (true-but-misleading framing) and is
  where a cheaper model most often regresses; weight the adversarial cases here.

### The bar a provider must meet before it runs a stage

A non-default provider (Gemini) may run a stage **only if**, on the live real-model
sweep over the golden set, it meets **both**:

- live **literal** accuracy at least the Haiku run's literal accuracy on the same
  sweep (and at least `baselineLiteralAccuracy`), **and**
- live **flag** accuracy at least the Haiku run's flag accuracy on the same sweep
  (and at least `baselineFlagAccuracy`),

with **no per-label collapse** (no literal label whose accuracy falls materially
below Haiku's on that label). Record both providers' numbers, the corpus, and the
date in the PR that proposes the switch. If Gemini misses the bar on either axis, the
stage stays on the Anthropic/Haiku default — the comparison is the gate, not a
preference. **This card changes no default**; `LLM_PROVIDER` stays `anthropic` until
a switch PR carries a passing real-model comparison.

## Flag decision: `FACTCHECK_POLITICAL` stays default OFF

The default is **NOT** flipped in this change. The offline eval is a faked-model
run; flipping the production default on the strength of a faked run alone would be
dishonest, because it measures the wiring, not whether the live retrieval plus the
live verifier hit the baseline on a real corpus. The flag remains **default off**
(`LoadPolitical` reads `FACTCHECK_POLITICAL`, default false) until a real-model
eval (above) is run and meets or beats the recorded baseline.

### How to flip the default once the bar is met

Only after step 4 shows the live run meets or beats both baselines:

1. Change `boolEnv("FACTCHECK_POLITICAL")` to default true in
   `internal/config/config.go` (`LoadPolitical`).
2. Update the config test that asserts the default is off
   (`TestLoadPolitical` in `internal/config/config_test.go`).
3. Record the real-model numbers (literal + flag accuracy, per-label breakdown,
   corpus and date) in the flipping PR.

Until then, the offline gate guards the wiring and the path stays opt-in via
`FACTCHECK_POLITICAL=true`.
