"""Fine-tune CamemBERTa-v2-base for check-worthiness classification.

A plain torch loop (no Trainer) keeps the procedure explicit and pinned:
weighted cross-entropy against the class imbalance, AdamW with linear warmup,
best-epoch selection on validation F1, then temperature fitting on the
validation logits. The chosen temperature ships inside the exported head (see
export.py), never as a runtime parameter.
"""

from __future__ import annotations

import json
import random
from dataclasses import dataclass
from pathlib import Path

MODEL_NAME = "almanach/camembertav2-base"
MAX_LENGTH = 128


@dataclass
class TrainConfig:
    data_dir: Path
    out_dir: Path
    epochs: int = 3
    batch_size: int = 16
    learning_rate: float = 2e-5
    warmup_fraction: float = 0.1
    seed: int = 42


def _read_jsonl(path: Path) -> tuple[list[str], list[int]]:
    texts: list[str] = []
    labels: list[int] = []
    with path.open(encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            texts.append(row["text"])
            labels.append(int(row["label"]))
    return texts, labels


def f1_binary(predictions: list[int], labels: list[int]) -> float:
    tp = sum(1 for p, y in zip(predictions, labels) if p == 1 and y == 1)
    fp = sum(1 for p, y in zip(predictions, labels) if p == 1 and y == 0)
    fn = sum(1 for p, y in zip(predictions, labels) if p == 0 and y == 1)
    denom = 2 * tp + fp + fn
    return 2 * tp / denom if denom else 0.0


def run(cfg: TrainConfig) -> dict:
    import numpy as np
    import torch
    from torch.utils.data import DataLoader
    from transformers import AutoModelForSequenceClassification, AutoTokenizer, get_linear_schedule_with_warmup

    from .calibrate import expected_calibration_error, fit_temperature

    random.seed(cfg.seed)
    np.random.seed(cfg.seed)
    torch.manual_seed(cfg.seed)

    device = torch.device("mps" if torch.backends.mps.is_available() else "cuda" if torch.cuda.is_available() else "cpu")

    train_texts, train_labels = _read_jsonl(cfg.data_dir / "train.jsonl")
    val_texts, val_labels = _read_jsonl(cfg.data_dir / "val.jsonl")

    tokenizer = AutoTokenizer.from_pretrained(MODEL_NAME)
    model = AutoModelForSequenceClassification.from_pretrained(MODEL_NAME, num_labels=2)
    model.to(device)

    def encode(texts: list[str]) -> dict:
        return tokenizer(texts, truncation=True, max_length=MAX_LENGTH, padding=True, return_tensors="pt")

    positives = sum(train_labels)
    negatives = len(train_labels) - positives
    total = len(train_labels)
    class_weights = torch.tensor(
        [total / (2 * max(1, negatives)), total / (2 * max(1, positives))],
        dtype=torch.float32,
        device=device,
    )
    loss_fn = torch.nn.CrossEntropyLoss(weight=class_weights)

    indices = list(range(len(train_texts)))
    loader = DataLoader(indices, batch_size=cfg.batch_size, shuffle=True, generator=torch.Generator().manual_seed(cfg.seed))

    steps = max(1, len(loader)) * cfg.epochs
    optimizer = torch.optim.AdamW(model.parameters(), lr=cfg.learning_rate)
    scheduler = get_linear_schedule_with_warmup(optimizer, int(steps * cfg.warmup_fraction), steps)

    def evaluate() -> tuple[list[tuple[float, float]], list[int]]:
        model.eval()
        logit_pairs: list[tuple[float, float]] = []
        with torch.no_grad():
            for start in range(0, len(val_texts), cfg.batch_size):
                batch = encode(val_texts[start : start + cfg.batch_size]).to(device)
                logits = model(**batch).logits.float().cpu().tolist()
                logit_pairs.extend((row[0], row[1]) for row in logits)
        return logit_pairs, val_labels

    best_f1 = -1.0
    best_state: dict | None = None
    history: list[dict] = []
    for epoch in range(cfg.epochs):
        model.train()
        epoch_loss = 0.0
        for batch_indices in loader:
            batch_texts = [train_texts[i] for i in batch_indices.tolist()]
            batch_labels = torch.tensor([train_labels[i] for i in batch_indices.tolist()], device=device)
            batch = encode(batch_texts).to(device)
            optimizer.zero_grad()
            logits = model(**batch).logits
            loss = loss_fn(logits, batch_labels)
            loss.backward()
            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            optimizer.step()
            scheduler.step()
            epoch_loss += loss.item()

        logit_pairs, labels = evaluate()
        predictions = [1 if pos > neg else 0 for neg, pos in logit_pairs]
        f1 = f1_binary(predictions, labels)
        accuracy = sum(1 for p, y in zip(predictions, labels) if p == y) / len(labels)
        history.append({"epoch": epoch + 1, "train_loss": epoch_loss / max(1, len(loader)), "val_f1": f1, "val_accuracy": accuracy})
        if f1 > best_f1:
            best_f1 = f1
            best_state = {k: v.detach().cpu().clone() for k, v in model.state_dict().items()}

    if best_state is not None:
        model.load_state_dict(best_state)
    model.to(device)

    logit_pairs, labels = evaluate()
    temperature = fit_temperature(logit_pairs, labels)

    def prob(neg: float, pos: float, t: float) -> float:
        import math

        m = max(neg / t, pos / t)
        en = math.exp(neg / t - m)
        ep = math.exp(pos / t - m)
        return ep / (en + ep)

    raw_probs = [prob(neg, pos, 1.0) for neg, pos in logit_pairs]
    cal_probs = [prob(neg, pos, temperature) for neg, pos in logit_pairs]
    metrics = {
        "device": str(device),
        "train_size": len(train_texts),
        "train_positives": positives,
        "train_negatives": negatives,
        "val_size": len(val_texts),
        "history": history,
        "best_val_f1": best_f1,
        "temperature": temperature,
        "ece_before_calibration": expected_calibration_error(raw_probs, labels),
        "ece_after_calibration": expected_calibration_error(cal_probs, labels),
    }

    model_dir = cfg.out_dir / "model"
    model_dir.mkdir(parents=True, exist_ok=True)
    model.cpu().save_pretrained(model_dir)
    tokenizer.save_pretrained(model_dir)
    (cfg.out_dir / "temperature.json").write_text(json.dumps({"temperature": temperature}, indent=2))
    (cfg.out_dir / "train_metrics.json").write_text(json.dumps(metrics, indent=2, ensure_ascii=False))
    return metrics
