# French political golden eval — real-model run and `FACTCHECK_POLITICAL` flip

This package holds the offline golden-eval gate for the French political two-axis
fact-check path (`internal/verify.VerifyPolitical`). The gate is deterministic and
runs in CI with no external API and no database; it guards the wiring and the
citation/flag logic, not live model quality. This document records how to run the
*real-model* eval and the decision rule for flipping the `FACTCHECK_POLITICAL`
production default.

## What the offline gate proves (and does not)

`TestGoldenEvalAccuracyGate` runs every case in `testdata/golden.json` through the
real `verify.Client` against a fake Anthropic server that replays each case's
recorded `record_assessment` tool-call. It asserts two recorded baselines:

- `baselineLiteralAccuracy` — the fraction of cases whose literal verdict
  (`accurate | inaccurate | unverifiable`) matches the label.
- `baselineFlagAccuracy` — the fraction of cases whose surviving manipulation-flag
  set exactly equals the expected flag set.

Because the model output is recorded, the gate is a **regression guard on the
two-axis wiring and the deterministic citation/flag guard**, not a measurement of
how well a live model labels French political speech. A change that breaks
verdict mapping, citation grounding, or flag sanitization drops the measured
accuracy below the recorded baseline and fails the gate. The current recorded
baseline is **100% literal / 100% flag** over 38 cases, because every recorded
verdict is authored to the label and every citation span is an exact substring of
its passage (so the guard keeps it).

The offline gate alone is **not** sufficient evidence to flip the production
default: it does not exercise live `voyage-4-large` retrieval or the live Claude
verifier on a real corpus.

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
