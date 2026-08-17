# Check-worthiness gate training pipeline (VER-225)

Trains, calibrates, and exports the local French check-worthiness classifier
that scores statements between the deterministic heuristic and the generative
gate. Clear cases are decided locally on CPU; only calibrated scores inside
the configured uncertainty band still reach the generative model.

## Serving substrate decision (recorded 2026-08-16)

**In-process Go inference** via `github.com/yalue/onnxruntime_go` v1.33.0
(ONNX Runtime C API 1.29.0) plus `github.com/daulet/tokenizers` v1.27.0 (Rust
FFI binding of the HuggingFace tokenizers; CamemBERTa-v2 ships a standard
WordPiece `tokenizer.json` it loads directly). A Python sidecar was rejected:
a second container to build, patch, health-check, and scale, plus a network
hop per statement, buys nothing for a ~111M-parameter encoder that infers in
single-digit milliseconds on CPU.

Measured on an Apple M5 Pro (darwin/arm64, INT8 export, sequence <= 64,
batch 1): **7.8 ms mean per statement** over 20 calls through the Go scorer
(`go test -tags localinference ./internal/localworthy/ -run Latency`). The
fp32 export parity against the torch reference is exact to 1.4e-6; the INT8
logit drift is checked against the golden set by the evaluate step.

Both native libraries need cgo, so the scorer compiles only under the
`localinference` build tag; the default pure-Go build (CI, current prod image)
ships a stub whose constructor reports the scorer unavailable and the backend
keeps today's heuristic-plus-model cascade. Enabling the gate in an image
means building with `-tags localinference`, `CGO_ENABLED=1`, `libtokenizers.a`
on the linker path, and the ONNX Runtime shared library alongside the binary
(`CHECKWORTHINESS_LOCAL_ONNX_LIBRARY`).

Pinned training stack (uv.lock is authoritative): torch 2.13.0, transformers
4.57.6, optimum 2.1.0 + optimum-onnx 0.1.0 (opset 18), onnxruntime 1.28.0
(export/quantize only; the Go side loads 1.29.0), psycopg 3.3.4.

## Data

- **Positives**: ClaimReview-derived rows in `political_claims` (statements a
  professional fact-checker chose to check), transcript statements the
  pipeline verified, and `data/annotated_positives.jsonl` (spoken-register
  claims).
- **Negatives**: transcript statements the live gate skipped as
  `not_a_claim` (from `claim_checks` telemetry and stored analyses),
  `document_sentences` skips, and `data/annotated_negatives.jsonl` - opinions,
  exhortations, predictions, anecdotes, and rhetoric written to *survive the
  deterministic heuristic*, because only heuristic survivors ever reach this
  classifier at runtime.
- **Golden holdout**: `data/golden.jsonl`, hand-reviewed, never trains; any
  training row colliding with it (accent/punctuation-folded) is dropped.
  Label semantics mirror the generative gate's prompt: when unsure, a
  statement is not check-worthy.

## Retrain procedure

Postgres must be up with the ClaimReview corpus ingested
(`make factcheck-workers` + `make factcheck-crawl`). Then:

    uv sync
    uv run checkworthy-gate build-dataset --dsn "$DATABASE_URL" --out out/data
    uv run checkworthy-gate train --data out/data --out out/train
    uv run checkworthy-gate export --train out/train --out out/export
    DEEPSEEK_API_KEY=... uv run checkworthy-gate record-llm --out out/llm_labels.json
    uv run checkworthy-gate evaluate --export out/export \
        --fixture ../../backend/internal/eval/testdata/gate_golden.json \
        --band-low 0.35 --band-high 0.75

Every step is deterministic under the pinned seed (42). `evaluate` prints the
acceptance metrics (outside-band agreement with the generative gate, model
call rate, cascade accuracy) and regenerates the committed eval fixture that
CI gates via `cmd/eval`; commit the fixture with the retrain. Calibration is
temperature scaling fitted on validation logits and folded into the exported
head (weights and bias divided by T), so the runtime applies a plain softmax.

Artifacts under `out/` never enter git. Distribute `out/export/model.int8.onnx`
and `out/export/onnx/tokenizer.json` via the container image or object
storage, and point `CHECKWORTHINESS_LOCAL_MODEL_PATH` /
`CHECKWORTHINESS_LOCAL_TOKENIZER_PATH` at them.

## Tests

    python3 -m unittest discover -s tests

Pure logic only (no torch, no database, no network); CI runs it on every PR
(`checkworthy-gate-test` in pr.yml). The Go scorer has its own tagged
integration suite in `stack/backend/internal/localworthy`.

## NLI stance stage (VER-228)

The stance scorer is the pretrained `almanach/camembertav2-base-xnli`
cross-encoder, consumed as the community ONNX export
(`onnx-community/camembertav2-base-xnli`) - DeBERTa's custom ops have a
history of export friction and the community artifact is already validated.
Labels: index 0 entailment, 1 neutral, 2 contradiction. FEVER convention:
evidence passage = premise, claim = hypothesis. The Go binding has no pair
API, so the runtime assembles `[CLS] premise [SEP] [SEP] hypothesis [SEP]`
by token ids; `nli-fetch` proves that assembly identical to the reference
tokenizer's own pair encoding before anything ships.

    uv run checkworthy-gate nli-fetch --out out/nli
    uv run checkworthy-gate nli-calibrate --entail-threshold 0.70 --contradict-threshold 0.90

Calibration (recorded 2026-08-16): temperature 1.8634 fitted by 3-class NLL
over 119 labeled French pairs (the committed verdict golden set flattened to
claim/passage pairs plus `data/nli_pairs.jsonl`, which carries 12 negation
mirror pairs and 12 neutrals). At the shipped thresholds the consensus rule
decides 53.9 percent of cases locally with 100 percent decided accuracy and
zero negation violations; the contradiction bar is deliberately higher than
the entailment bar because wrongly refuting a claim costs more than
escalating it. The artifact is upstream, so the temperature ships as
configuration (`FACTCHECK_NLI_TEMPERATURE`), not folded into weights.
`nli-calibrate` regenerates the committed eval fixture
(`backend/internal/eval/testdata/nli_golden.json`) that CI replays offline.

Measured through the Go scorer (M5 Pro, INT8): 27 ms per claim against three
passages (`go test -tags localinference ./internal/nli/ -run Latency`).
