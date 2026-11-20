"""NLI stance model: fetch, calibration, and eval fixture generation (VER-228).

The stance scorer is the pretrained `almanach/camembertav2-base-xnli`
cross-encoder, consumed as the community ONNX export
(`onnx-community/camembertav2-base-xnli`) rather than re-exported - DeBERTa's
custom ops have a history of export friction and the community artifact is
already validated. Labels are index 0 entailment, 1 neutral, 2 contradiction;
the FEVER convention applies: the evidence passage is the premise, the claim
the hypothesis.

Calibration fits a single softmax temperature on labeled French
claim/passage pairs (derived from the committed verdict golden set plus the
authored pair fixture), then sweeps the consensus thresholds and records
everything the Go side needs: the temperature ships as configuration (the
artifact is upstream, so nothing is folded into weights), and the committed
eval fixture records per-pair calibrated probabilities so CI replays the
consensus rule offline.
"""

from __future__ import annotations

import json
import math
from dataclasses import dataclass, field
from pathlib import Path

NLI_REPO = "onnx-community/camembertav2-base-xnli"
NLI_FILES = ["onnx/model_int8.onnx", "tokenizer.json", "config.json", "special_tokens_map.json", "tokenizer_config.json"]
IDX_ENTAILMENT, IDX_NEUTRAL, IDX_CONTRADICTION = 0, 1, 2
LABELS = {"entailment": IDX_ENTAILMENT, "neutral": IDX_NEUTRAL, "contradiction": IDX_CONTRADICTION}
MAX_PAIR_TOKENS = 256


@dataclass
class Pair:
    premise: str
    hypothesis: str
    label: int
    case_id: str = ""
    passage_id: str = ""


@dataclass
class Case:
    """One claim with its passages, for consensus-level evaluation."""

    case_id: str
    claim: str
    label: str
    passage_ids: list[str] = field(default_factory=list)
    pair_indices: list[int] = field(default_factory=list)
    negation_of: str = ""


def fetch(out_dir: Path) -> dict:
    """Download the community ONNX artifact and prove the pair template.

    The template proof matters because the Go tokenizer binding has no pair
    API: the runtime assembles `[CLS] premise [SEP] [SEP] hypothesis [SEP]`
    from single-sequence encodes, and this step asserts that assembly matches
    the reference tokenizer's own pair encoding exactly.
    """
    from huggingface_hub import hf_hub_download
    from transformers import AutoTokenizer

    out_dir.mkdir(parents=True, exist_ok=True)
    for name in NLI_FILES:
        hf_hub_download(NLI_REPO, name, local_dir=out_dir)

    config = json.loads((out_dir / "config.json").read_text())
    id2label = {int(k): v for k, v in config["id2label"].items()}
    expected = {IDX_ENTAILMENT: "entailment", IDX_NEUTRAL: "neutral", IDX_CONTRADICTION: "contradiction"}
    if id2label != expected:
        raise RuntimeError(f"unexpected id2label {id2label}, wanted {expected}")

    tokenizer = AutoTokenizer.from_pretrained(out_dir)
    premise = "Le chômage a baissé de deux points cette année selon l'institut national."
    hypothesis = "Le chômage baisse en France."
    reference = tokenizer(premise, hypothesis)["input_ids"]
    p = tokenizer(premise, add_special_tokens=False)["input_ids"]
    h = tokenizer(hypothesis, add_special_tokens=False)["input_ids"]
    cls_id, sep_id = tokenizer.cls_token_id, tokenizer.sep_token_id
    assembled = [cls_id] + p + [sep_id, sep_id] + h + [sep_id]
    if assembled != reference:
        raise RuntimeError("manual pair assembly does not match the reference tokenizer pair encoding")

    report = {
        "repo": NLI_REPO,
        "model": str(out_dir / "onnx" / "model_int8.onnx"),
        "tokenizer": str(out_dir / "tokenizer.json"),
        "cls_token_id": cls_id,
        "sep_token_id": sep_id,
        "pair_template_verified": True,
        "id2label": {str(k): v for k, v in id2label.items()},
    }
    (out_dir / "fetch_report.json").write_text(json.dumps(report, indent=2))
    return report


def derive_pairs_from_golden(golden_path: Path) -> tuple[list[Pair], list[Case]]:
    """Flatten the committed verdict golden set into labeled NLI pairs.

    An accurate claim is entailed by its relevant passages; an inaccurate
    claim is contradicted by them; an unverifiable claim's passages are
    neutral. Passages outside a case's relevant set are retrieval distractors,
    neutral by construction.
    """
    doc = json.loads(golden_path.read_text())
    pairs: list[Pair] = []
    cases: list[Case] = []
    label_by_literal = {"accurate": "support", "inaccurate": "refute", "unverifiable": "neutral"}
    for case in doc["cases"]:
        literal = case["expected_literal"]
        relevant = set(case.get("relevant") or [p["id"] for p in case["passages"]])
        c = Case(case_id=case["id"], claim=case["statement"], label=label_by_literal[literal])
        for passage in case["passages"]:
            if passage["id"] in relevant:
                pair_label = {"accurate": IDX_ENTAILMENT, "inaccurate": IDX_CONTRADICTION, "unverifiable": IDX_NEUTRAL}[literal]
            else:
                pair_label = IDX_NEUTRAL
            c.pair_indices.append(len(pairs))
            c.passage_ids.append(passage["id"])
            pairs.append(Pair(premise=passage["text"], hypothesis=case["statement"], label=pair_label, case_id=case["id"], passage_id=passage["id"]))
        cases.append(c)
    return pairs, cases


def load_pair_fixture(path: Path) -> tuple[list[Pair], list[Case]]:
    """Load the authored pair fixture, one case per line.

    Each line: {"id", "claim", "premise", "label": support|refute|neutral,
    "negation_of": optional id of the mirrored case}. Single-passage cases by
    construction; the premise doubles as the passage.
    """
    pairs: list[Pair] = []
    cases: list[Case] = []
    pair_label = {"support": IDX_ENTAILMENT, "refute": IDX_CONTRADICTION, "neutral": IDX_NEUTRAL}
    with path.open(encoding="utf-8") as fh:
        for line_no, line in enumerate(fh, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            if row["label"] not in pair_label:
                raise ValueError(f"{path}:{line_no}: unknown label {row['label']!r}")
            c = Case(case_id=row["id"], claim=row["claim"], label=row["label"], negation_of=row.get("negation_of", ""))
            c.pair_indices.append(len(pairs))
            c.passage_ids.append(row["id"] + "-p")
            pairs.append(Pair(premise=row["premise"], hypothesis=row["claim"], label=pair_label[row["label"]], case_id=row["id"], passage_id=row["id"] + "-p"))
            cases.append(c)
    return pairs, cases


def score_pairs(model_dir: Path, pairs: list[Pair]) -> list[list[float]]:
    """Raw logits for every (premise, hypothesis) pair through the INT8 model."""
    import numpy as np
    import onnxruntime
    from transformers import AutoTokenizer

    tokenizer = AutoTokenizer.from_pretrained(model_dir)
    session = onnxruntime.InferenceSession(str(model_dir / "onnx" / "model_int8.onnx"), providers=["CPUExecutionProvider"])
    input_names = {i.name for i in session.get_inputs()}
    logits: list[list[float]] = []
    for pair in pairs:
        batch = tokenizer(pair.premise, pair.hypothesis, truncation=True, max_length=MAX_PAIR_TOKENS, return_tensors="np")
        feeds = {name: batch[name].astype(np.int64) for name in input_names if name in batch}
        out = session.run(None, feeds)[0][0]
        logits.append([float(v) for v in out])
    return logits


def softmax3(logits: list[float], temperature: float) -> list[float]:
    scaled = [v / temperature for v in logits]
    m = max(scaled)
    exps = [math.exp(v - m) for v in scaled]
    total = sum(exps)
    return [e / total for e in exps]


def nll3(logits: list[list[float]], labels: list[int], temperature: float) -> float:
    if len(logits) != len(labels) or not labels:
        raise ValueError("logits and labels must be non-empty and aligned")
    total = 0.0
    for row, label in zip(logits, labels):
        total -= math.log(max(softmax3(row, temperature)[label], 1e-12))
    return total / len(labels)


def fit_temperature3(logits: list[list[float]], labels: list[int]) -> float:
    """Golden-section search over the 3-class NLL, mirroring the binary fit."""
    inv_phi = (math.sqrt(5) - 1) / 2
    a, b = 0.05, 10.0
    c = b - inv_phi * (b - a)
    d = a + inv_phi * (b - a)
    fc = nll3(logits, labels, c)
    fd = nll3(logits, labels, d)
    for _ in range(200):
        if b - a < 1e-6:
            break
        if fc < fd:
            b, d, fd = d, c, fc
            c = b - inv_phi * (b - a)
            fc = nll3(logits, labels, c)
        else:
            a, c, fc = c, d, fd
            d = a + inv_phi * (b - a)
            fd = nll3(logits, labels, d)
    return (a + b) / 2


def decide(case_probs: list[list[float]], entail_threshold: float, contradict_threshold: float, min_agree: int) -> str:
    """The conservative consensus rule, mirrored exactly by the Go stage.

    A case is decided support only when at least min_agree passages entail
    above threshold and no passage contradicts above its threshold; refute is
    symmetric; anything else escalates.
    """
    entails = sum(1 for p in case_probs if p[IDX_ENTAILMENT] >= entail_threshold)
    contradicts = sum(1 for p in case_probs if p[IDX_CONTRADICTION] >= contradict_threshold)
    if entails >= min_agree and contradicts == 0:
        return "support"
    if contradicts >= min_agree and entails == 0:
        return "refute"
    return "escalate"


def consensus_metrics(cases: list[Case], probs: list[list[float]], entail_threshold: float, contradict_threshold: float, min_agree: int) -> dict:
    decided = 0
    correct = 0
    wrong: list[str] = []
    for case in cases:
        verdict = decide([probs[i] for i in case.pair_indices], entail_threshold, contradict_threshold, min_agree)
        if verdict == "escalate":
            continue
        decided += 1
        if verdict == case.label:
            correct += 1
        else:
            wrong.append(f"{case.case_id}: decided {verdict}, labeled {case.label}")
    return {
        "decided": decided,
        "total": len(cases),
        "local_share": decided / len(cases) if cases else 0.0,
        "decided_accuracy": correct / decided if decided else 1.0,
        "wrong": wrong,
    }


def negation_violations(cases: list[Case], probs: list[list[float]], entail_threshold: float, contradict_threshold: float, min_agree: int) -> list[str]:
    """A claim and its negation must never get the same decided stance."""
    by_id = {c.case_id: c for c in cases}
    violations: list[str] = []
    for case in cases:
        if not case.negation_of or case.negation_of not in by_id:
            continue
        other = by_id[case.negation_of]
        mine = decide([probs[i] for i in case.pair_indices], entail_threshold, contradict_threshold, min_agree)
        theirs = decide([probs[i] for i in other.pair_indices], entail_threshold, contradict_threshold, min_agree)
        if mine != "escalate" and mine == theirs:
            violations.append(f"{case.case_id} and its negation {other.case_id} both decided {mine}")
    return violations
