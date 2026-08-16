# Vector-first defaults (VER-230)

Since VER-230 the default configuration is the vector-first pipeline:
retrieve-then-verify with the local check-worthiness classifier, the NLI
stance scorer, and reranked hybrid retrieval all enabled, and generative
calls reserved for the calibrated grey zones. Every stage keeps its flag as a
documented kill-switch, and every stage with a secret or an artifact
dependency stays inactive (with a boot warning) until that dependency is
provided, so a keyless or artifact-less deployment degrades to the previous
behavior rather than failing.

## The coherent default set

One reviewed table instead of per-card leftovers. Sources of truth: the
`default*` constants in `stack/backend/internal/config/config.go` and the
committed calibration runs recorded in `stack/ml/checkworthy`.

| Stage | Flag (kill-switch) | Default | Key thresholds (default) |
|---|---|---|---|
| Hybrid retrieval | `MATCH_HYBRID_SEARCH` | on | RRF fusion; cosine orders survivors |
| Reranking | `MATCH_RERANK` | on | `rerank-2.5`, 20 candidates, 800ms budget; fail-open to fused order |
| Deterministic gate | `PRECHECK_ENABLED` | on | min words 4; French lexicons |
| Local check-worthiness band | `CHECKWORTHINESS_LOCAL_ENABLED` | on (needs artifacts) | band [0.35, 0.75); calibration folded into the exported head |
| Generative gate (band only) | `CHECKWORTHINESS_ENABLED` | on (needs key) | consulted only inside the band once the local scorer is active |
| Verify path | `FACTCHECK_VERIFY_PATH` | on (needs key) | fast-borrow tau 0.9; retrieval floor 0.45 |
| Political two-axis mode | `FACTCHECK_POLITICAL` | on (French locale) | curated tau, two-axis literal+flags verdicts |
| NLI stance stage | `FACTCHECK_NLI_ENABLED` | on (needs artifacts) | temperature 1.8634; entail >= 0.70, contradict >= 0.90, min agree 1, max 6 passages |
| Second pass | `FACTCHECK_SECOND_PASS` | off (opt-in) | trigger below 0.8, adopt at >= 0.9 |

The contradiction bar is deliberately higher than the entailment bar: wrongly
refuting a claim costs more than escalating it. The second pass stays opt-in
because it spends a reasoning call per weak verdict; the budget below prices
it when enabled.

## The generative-call budget

`go run ./cmd/eval` composes the predicted generative calls per checked claim
from the committed fixtures and fails when the total exceeds the committed
ceiling (`budget` key in `internal/eval/testdata/baseline.json`):

- gate stage: share of statements inside the local classifier's band (0.013
  at the current fixture);
- verdict stage: share of claims the NLI consensus escalates (0.461);
- second pass: share of locally-decided verdicts under the 0.8 trigger
  (0.067);
- **total 0.541, ceiling 0.65**. Claim decomposition stays one call per
  spoken unit by design and is outside this ratio.

The live ground truth remains the `claim_checks` telemetry (`llm_calls`
column, `decision_path` per stage); the fixture-based prediction is what CI
can enforce offline.

## Measured comparison (recorded 2026-08-16)

`go run -tags localinference ./cmd/eval -compare-defaults` runs the two
decision stages over the committed French fixtures with live models
(DeepSeek + the shipped INT8 artifacts, Apple M5 Pro) under both
configurations:

| Stage | Configuration | Accuracy | Generative calls | Mean latency |
|---|---|---|---|---|
| Gate (159 statements) | legacy (heuristic + generative gate) | 1.000 | 159 (1.000/case) | 1.212s |
| | vector-first (local band) | 1.000 | 2 (0.013/case) | 23ms |
| Verdict (89 claims) | legacy (generative verifier) | 0.944 | 89 (1.000/case) | 1.655s |
| | vector-first (NLI consensus first) | 0.944 | 41 (0.461/case) | 780ms |

Total: 248 -> 43 generative calls, an **83 percent reduction at identical
accuracy**, with per-statement latency cut 53x at the gate and 2.1x at the
verdict stage. The streaming transcription leg is configuration-invariant and
not part of the comparison; retrieval quality is covered by the retrieval
gate and the `-rerank` comparison (VER-226: recall@1 93.3% -> 100% measured
live).

## Operating the artifacts

The local scorers need a `localinference`-tagged binary (cgo, ONNX Runtime
1.29.0, `libtokenizers.a`) plus the model artifacts distributed outside git:

- check-worthiness: `stack/ml/checkworthy` trains and exports
  `model.int8.onnx` + `tokenizer.json` (see its README for the retrain
  procedure);
- NLI: `nli-fetch` downloads the community export of
  `camembertav2-base-xnli`.

Point `CHECKWORTHINESS_LOCAL_*` and `FACTCHECK_NLI_*` at the artifacts. The
default pure-Go build ships fail-open stubs, so an untagged binary simply
keeps the generative-first behavior with a boot warning.
