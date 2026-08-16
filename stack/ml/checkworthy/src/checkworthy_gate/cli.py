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

    args = parser.parse_args()
    return args.fn(args)


if __name__ == "__main__":
    raise SystemExit(main())
