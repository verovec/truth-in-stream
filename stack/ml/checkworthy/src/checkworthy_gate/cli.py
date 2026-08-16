"""Command-line entry points for the training pipeline.

    checkworthy-gate build-dataset --dsn postgres://... --out out/data
    checkworthy-gate train --data out/data --out out/train
    checkworthy-gate export --train out/train --out out/export
    checkworthy-gate record-llm --golden data/golden.jsonl --out out/llm_labels.json
    checkworthy-gate evaluate --export out/export --fixture ../../backend/internal/eval/testdata/gate_golden.json

Each step is reproducible from its inputs; run them in order for a full
retrain. Artifacts under out/ never enter git.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path

from . import dataset as ds

PACKAGE_DIR = Path(__file__).resolve().parent
PROJECT_DIR = PACKAGE_DIR.parent.parent
DATA_DIR = PROJECT_DIR / "data"
SEED = 42
MAX_CLASS_RATIO = 3.0
VAL_FRACTION = 0.1


def cmd_build_dataset(args: argparse.Namespace) -> int:
    golden = ds.load_golden(DATA_DIR / "golden.jsonl")
    pools = ds.load_postgres_pools(args.dsn)
    annotated_negatives = ds.load_jsonl(DATA_DIR / "annotated_negatives.jsonl", ds.LABEL_NOT_CLAIM, "annotated")
    annotated_positives = ds.load_jsonl(DATA_DIR / "annotated_positives.jsonl", ds.LABEL_CLAIM, "annotated")

    merged = ds.merge(
        [
            annotated_negatives,
            annotated_positives,
            pools["telemetry_skips"],
            pools["transcript_skips"],
            pools["document_skips"],
            pools["telemetry_claims"],
            pools["transcript_claims"],
            pools["claimreview"],
        ],
        golden,
    )
    balanced = ds.balance(merged, MAX_CLASS_RATIO, SEED)
    train, val = ds.split(balanced, VAL_FRACTION, SEED)

    out = Path(args.out)
    ds.write_jsonl(out / "train.jsonl", train)
    ds.write_jsonl(out / "val.jsonl", val)
    stats = {
        "pools": {name: len(pool) for name, pool in pools.items()},
        "annotated_negatives": len(annotated_negatives),
        "annotated_positives": len(annotated_positives),
        "merged": len(merged),
        "balanced": len(balanced),
        "train": len(train),
        "val": len(val),
        "train_positives": sum(1 for e in train if e.label == ds.LABEL_CLAIM),
        "train_negatives": sum(1 for e in train if e.label == ds.LABEL_NOT_CLAIM),
        "golden": len(golden),
    }
    (out / "dataset_stats.json").write_text(json.dumps(stats, indent=2))
    print(json.dumps(stats, indent=2))
    return 0


def cmd_train(args: argparse.Namespace) -> int:
    from .train import TrainConfig, run

    metrics = run(TrainConfig(data_dir=Path(args.data), out_dir=Path(args.out), epochs=args.epochs, seed=SEED))
    print(json.dumps({k: v for k, v in metrics.items() if k != "history"}, indent=2))
    return 0


def cmd_export(args: argparse.Namespace) -> int:
    from .export import ExportConfig, run

    report = run(ExportConfig(train_dir=Path(args.train), out_dir=Path(args.out)))
    print(json.dumps(report, indent=2))
    return 0


def cmd_record_llm(args: argparse.Namespace) -> int:
    from .llmgate import check_worthy

    api_key = os.environ.get("DEEPSEEK_API_KEY", "")
    if not api_key:
        print("DEEPSEEK_API_KEY is required", file=sys.stderr)
        return 1
    golden = ds.load_golden(Path(args.golden))
    labels: list[bool] = []
    for i, example in enumerate(golden):
        labels.append(check_worthy(api_key, example.text))
        if (i + 1) % 20 == 0:
            print(f"{i + 1}/{len(golden)} recorded", file=sys.stderr)
    Path(args.out).write_text(json.dumps(labels))
    print(f"recorded {len(labels)} verdicts to {args.out}")
    return 0


def cmd_evaluate(args: argparse.Namespace) -> int:
    from .evaluate import EvaluateConfig, run

    report = run(
        EvaluateConfig(
            golden_path=DATA_DIR / "golden.jsonl",
            export_dir=Path(args.export),
            fixture_path=Path(args.fixture),
            band_low=args.band_low,
            band_high=args.band_high,
            llm_labels_path=Path(args.llm_labels) if args.llm_labels else None,
        )
    )
    print(json.dumps(report, indent=2))
    return 0


def cmd_nli_fetch(args: argparse.Namespace) -> int:
    from .nli import fetch

    report = fetch(Path(args.out))
    print(json.dumps(report, indent=2))
    return 0


def cmd_nli_calibrate(args: argparse.Namespace) -> int:
    from . import nli

    model_dir = Path(args.model_dir)
    golden_pairs, golden_cases = nli.derive_pairs_from_golden(Path(args.golden))
    fixture_pairs, fixture_cases = nli.load_pair_fixture(DATA_DIR / "nli_pairs.jsonl")
    offset = len(golden_pairs)
    for case in fixture_cases:
        case.pair_indices = [i + offset for i in case.pair_indices]
    pairs = golden_pairs + fixture_pairs
    cases = golden_cases + fixture_cases

    logits = nli.score_pairs(model_dir, pairs)
    labels = [p.label for p in pairs]
    temperature = nli.fit_temperature3(logits, labels)
    probs = [nli.softmax3(row, temperature) for row in logits]

    pair_accuracy = sum(1 for p, y in zip(probs, labels) if max(range(3), key=lambda i: p[i]) == y) / len(labels)
    sweep = []
    for entail in (0.7, 0.8, 0.85, 0.9, 0.95):
        for contradict in (0.7, 0.8, 0.85, 0.9, 0.95):
            metrics = nli.consensus_metrics(cases, probs, entail, contradict, args.min_agree)
            violations = nli.negation_violations(cases, probs, entail, contradict, args.min_agree)
            sweep.append({"entail": entail, "contradict": contradict, **{k: v for k, v in metrics.items() if k != "wrong"}, "negation_violations": len(violations)})

    chosen = nli.consensus_metrics(cases, probs, args.entail_threshold, args.contradict_threshold, args.min_agree)
    violations = nli.negation_violations(cases, probs, args.entail_threshold, args.contradict_threshold, args.min_agree)

    fixture_rows = []
    for case in cases:
        fixture_rows.append(
            {
                "id": case.case_id,
                "claim": case.claim,
                "label": case.label,
                "negation_of": case.negation_of,
                "passage_ids": case.passage_ids,
                "probs": [[round(v, 6) for v in probs[i]] for i in case.pair_indices],
            }
        )
    fixture = {
        "_about": "NLI stance golden set (VER-228): per-passage calibrated probabilities of "
        "entailment/neutral/contradiction for every case, recorded from the shipped INT8 artifact at the "
        "committed temperature. The Go eval replays the consensus rule offline. Regenerated by the "
        "training pipeline (stack/ml/checkworthy, nli-calibrate); never edited by hand.",
        "temperature": round(temperature, 6),
        "entail_threshold": args.entail_threshold,
        "contradict_threshold": args.contradict_threshold,
        "min_agree": args.min_agree,
        "cases": fixture_rows,
    }
    fixture_path = Path(args.fixture)
    fixture_path.parent.mkdir(parents=True, exist_ok=True)
    fixture_path.write_text(json.dumps(fixture, indent=2, ensure_ascii=False) + "\n")

    report = {
        "pairs": len(pairs),
        "cases": len(cases),
        "temperature": temperature,
        "pair_accuracy_at_argmax": pair_accuracy,
        "chosen": {"entail_threshold": args.entail_threshold, "contradict_threshold": args.contradict_threshold, "min_agree": args.min_agree, **chosen},
        "negation_violations": violations,
        "sweep": sweep,
    }
    (model_dir / "calibration.json").write_text(json.dumps(report, indent=2, ensure_ascii=False))
    print(json.dumps({k: v for k, v in report.items() if k != "sweep"}, indent=2, ensure_ascii=False))
    print("sweep:")
    for row in report["sweep"]:
        print(f"  e>={row['entail']:.2f} c>={row['contradict']:.2f} share={row['local_share']:.3f} acc={row['decided_accuracy']:.3f} negviol={row['negation_violations']}")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(prog="checkworthy-gate")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("build-dataset", help="build train/val sets from Postgres and committed fixtures")
    p.add_argument("--dsn", default=os.environ.get("DATABASE_URL", ""))
    p.add_argument("--out", default="out/data")
    p.set_defaults(fn=cmd_build_dataset)

    p = sub.add_parser("train", help="fine-tune CamemBERTa-v2 and fit the calibration temperature")
    p.add_argument("--data", default="out/data")
    p.add_argument("--out", default="out/train")
    p.add_argument("--epochs", type=int, default=3)
    p.set_defaults(fn=cmd_train)

    p = sub.add_parser("export", help="fold calibration into the head and export ONNX fp32 + INT8")
    p.add_argument("--train", default="out/train")
    p.add_argument("--out", default="out/export")
    p.set_defaults(fn=cmd_export)

    p = sub.add_parser("record-llm", help="snapshot the generative gate's verdicts over the golden set")
    p.add_argument("--golden", default=str(DATA_DIR / "golden.jsonl"))
    p.add_argument("--out", default="out/llm_labels.json")
    p.set_defaults(fn=cmd_record_llm)

    p = sub.add_parser("evaluate", help="score the golden set and write the committed eval fixture")
    p.add_argument("--export", default="out/export")
    p.add_argument("--fixture", default="out/gate_golden.json")
    p.add_argument("--band-low", type=float, default=0.35)
    p.add_argument("--band-high", type=float, default=0.75)
    p.add_argument("--llm-labels", default="out/llm_labels.json")
    p.set_defaults(fn=cmd_evaluate)

    p = sub.add_parser("nli-fetch", help="download the community NLI ONNX artifact and verify the pair template")
    p.add_argument("--out", default="out/nli")
    p.set_defaults(fn=cmd_nli_fetch)

    p = sub.add_parser("nli-calibrate", help="calibrate the NLI temperature and write the committed stance fixture")
    p.add_argument("--model-dir", default="out/nli")
    p.add_argument("--golden", default="../../backend/internal/eval/testdata/golden.json")
    p.add_argument("--fixture", default="../../backend/internal/eval/testdata/nli_golden.json")
    p.add_argument("--entail-threshold", type=float, default=0.85)
    p.add_argument("--contradict-threshold", type=float, default=0.85)
    p.add_argument("--min-agree", type=int, default=1)
    p.set_defaults(fn=cmd_nli_calibrate)

    args = parser.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    raise SystemExit(main())
