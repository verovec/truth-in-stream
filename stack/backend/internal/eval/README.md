# Retrieval and verdict eval gate

This package is the offline evaluation gate for the fact-check retrieval and
verify layers. It exists so that every change to how the pipeline finds and judges
evidence — retrieval thresholds, indexes, hybrid search, caching, the verify path —
is proven against a committed French political/statistical golden set before it
merges, and search quality can only ratchet up: a change that regresses recall or
verdict accuracy fails its gate instead of shipping.

It has two axes, one golden set (`testdata/golden.json`), and one committed
baseline (`testdata/baseline.json`):

- **Retrieval recall** (this card, VER-191) — the deterministic lexical retrieval
  oracle ranks each case's candidate passages against the claim and we measure
  recall@1 per retrieval-stress category. This is the new gate.
- **Two-axis verdict accuracy** (from the verify-path work) — the real
  `verify.Client` replays each case's recorded model verdict through the live
  citation and flag guard, and we measure literal and flag accuracy. See
  `PROCEDURE.md` for the real-model run and the `FACTCHECK_POLITICAL` flip.

Both run with no network model API and no database. The golden passages are the
seeded corpus; the recorded verdicts make the verify path deterministic.

## The one command

```
make eval
```

(or, from `stack/backend`, `go run ./cmd/eval`). It scores the retrieval oracle
over the golden set, prints per-category recall@1 and recall@3, and exits non-zero
if any category — or the overall — recall@1 falls below its committed floor:

```
retrieval oracle recall over 15 cases: overall R@1 93.3%, R@3 100.0%
  number-precision R@1 100.0% R@3 100.0% (3 cases)
  named-entity     R@1 100.0% R@3 100.0% (3 cases)
  date-anchored    R@1 100.0% R@3 100.0% (3 cases)
  paraphrase       R@1 66.7%  R@3 100.0% (3 cases)
  near-miss        R@1 100.0% R@3 100.0% (3 cases)

PASS: retrieval recall meets every committed floor
```

The verdict axis runs as an ordinary Go test:

```
cd stack/backend && go test ./internal/eval/...
```

which covers both gates (`TestRetrievalRecallGate` and
`TestGoldenEvalAccuracyGate`), the fixture validators, and the two teeth tests
that prove each gate catches an injected regression.

## The five retrieval-stress categories

Dense embeddings handle some French political claims worst — exact numbers,
percentages, dates, named entities, accented forms — precisely what political
claims hinge on. Each retrieval case in `golden.json` carries a `category` and a
`relevant` set (the passage ids that are the true retrieval targets; the rest are
distractors the oracle must rank below them):

- **number-precision** — an exact figure (unemployment rate, deficit, SMIC).
  Distractors share the topic but carry a different number.
- **named-entity** — a politician or institution. Distractors name a different
  actor in the same context (e.g. Conseil constitutionnel vs Conseil d'État).
- **date-anchored** — a year or period. Distractors repeat the claim for a
  different year.
- **paraphrase** — the same fact reworded. The target states it in different
  words; distractors reuse the claim's surface vocabulary without the fact.
- **near-miss** — right entity, wrong number. The trap category: distractors match
  the claim's wording almost exactly but carry a different figure.

The retrieval oracle (`RankPassages`) is a fixed, offline lexical baseline —
French-normalized (diacritics stripped), numeric-token-weighted cosine — that is
the eval's yardstick, **not** production retrieval. The paraphrase floor is below
1.0 on purpose: a purely lexical baseline mis-ranks a reworded claim sharing no
distinctive number or entity with its evidence, which is exactly the gap the
upcoming hybrid dense+lexical retrieval must close. When live hybrid retrieval
lands, score live recall against these same cases and compare it to the oracle
floor.

## CI

`.github/workflows/retrieval-eval.yml` runs the one command plus the eval and
vectorbench tests, and is path-filtered to retrieval-affecting changes: the store
and its vector queries/migrations, the verify path, the embedding layer, the eval
harness, and the vectorbench harness. Make it a required status check in branch
protection so a recall regression blocks merge.

## Updating the baseline

The baseline is a single reviewed file, `testdata/baseline.json`. Moving any
number is a deliberate act that must be visible in the PR diff:

- **Ratchet up** when a retrieval or verify change genuinely improves a category:
  raise that floor to the new measured value in the same PR, so the improvement
  cannot silently regress later. Run `make eval` to read the new numbers.
- **Relax** a floor only with the reason recorded in the PR description (a case
  reauthored, a deliberate trade-off). Never lower a floor just to make a red gate
  green — investigate the regression first.
- The `verdict` floors mirror the two-axis accuracy constants the verify-path gate
  asserts (`baselineLiteralAccuracy`, `baselineFlagAccuracy`);
  `TestBaselineVerdictFloorsMatchGateConstants` fails if the file and the code
  drift apart, so change both together.

Adding golden cases is the other reviewed lever: keep them in the existing
`golden.json` format (a retrieval case needs a `category`, a `relevant` set that
resolves to its passages, and — like every case — a recorded verdict whose
citation span is an exact substring of the cited passage). `LoadGolden` rejects a
malformed case at load time, so an authoring slip is a hard error, not a skewed
number.

## vectorbench hand-off (index-level changes)

`internal/vectorbench` is the offline harness for **index-level** decisions
(HNSW vs binary-quantize+rerank vs partition-by-source; `hnsw.ef_search` and
`hnsw.iterative_scan` sweeps) measuring recall@k, p50/p95 latency, and footprint
over a deterministic synthetic corpus. It needs a throwaway pgvector database and
takes 15–30 minutes, so it is operator-run, not a CI gate. When a change is
index-level (a new index type, a quantization or partition change, an instance
resize), run it and record the verdict in the PR:

```
make bench-datastore                       # defaults: 100k x 1024-dim vectors
make bench-datastore BENCH_FLAGS="-rows=10000 -ef=40,100"   # a quick pass
```

It writes `stack/backend/vectorbench-report.md` (gitignored); paste the relevant
recall@k / latency rows into the PR and reference them next to this eval's recall
numbers. The datastore-scale verdict lives in `docs/datastore-scale-benchmark.md`.
