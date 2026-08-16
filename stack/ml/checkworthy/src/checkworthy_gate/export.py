"""Export the fine-tuned classifier to ONNX with calibration folded in.

Temperature scaling divides logits by a constant T, which is exactly
equivalent to dividing the final linear layer's weight and bias by T. Folding
that into the checkpoint before export means the shipped graph emits
calibrated logits directly: no temperature parameter exists at runtime, so
the Go scorer and the training pipeline cannot disagree about it.

The fp32 export is the reference; a dynamically-quantized INT8 copy is
produced alongside it and ships only if the golden-set evaluation holds (the
evaluate step compares both).
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

PARITY_SENTENCES = [
    "Le chômage a baissé de deux points depuis le début du quinquennat.",
    "Nous allons continuer à nous battre pour les Français.",
    "La dette publique dépasse les trois mille milliards d'euros.",
    "C'est une honte absolue ce qui se passe dans ce pays.",
]
PARITY_TOLERANCE = 2e-3


@dataclass
class ExportConfig:
    train_dir: Path
    out_dir: Path
    opset: int = 18


def fold_temperature(model_dir: Path, temperature: float, folded_dir: Path) -> None:
    """Write a copy of the checkpoint whose head emits logits / temperature."""
    import torch
    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    model = AutoModelForSequenceClassification.from_pretrained(model_dir)
    with torch.no_grad():
        model.classifier.weight.div_(temperature)
        model.classifier.bias.div_(temperature)
    folded_dir.mkdir(parents=True, exist_ok=True)
    model.save_pretrained(folded_dir)
    AutoTokenizer.from_pretrained(model_dir).save_pretrained(folded_dir)


def run(cfg: ExportConfig) -> dict:
    import numpy as np
    import onnxruntime
    import torch
    from onnxruntime.quantization import QuantType, quantize_dynamic
    from optimum.exporters.onnx import main_export
    from transformers import AutoModelForSequenceClassification, AutoTokenizer

    model_dir = cfg.train_dir / "model"
    temperature = json.loads((cfg.train_dir / "temperature.json").read_text())["temperature"]

    folded_dir = cfg.out_dir / "folded"
    fold_temperature(model_dir, temperature, folded_dir)

    onnx_dir = cfg.out_dir / "onnx"
    main_export(str(folded_dir), output=str(onnx_dir), task="text-classification", opset=cfg.opset)
    fp32_path = onnx_dir / "model.onnx"

    int8_path = cfg.out_dir / "model.int8.onnx"
    quantize_dynamic(str(fp32_path), str(int8_path), weight_type=QuantType.QInt8)

    tokenizer = AutoTokenizer.from_pretrained(folded_dir)
    reference = AutoModelForSequenceClassification.from_pretrained(folded_dir)
    reference.eval()

    def torch_logits(text: str) -> np.ndarray:
        batch = tokenizer(text, truncation=True, max_length=128, return_tensors="pt")
        with torch.no_grad():
            return reference(**batch).logits[0].numpy()

    def onnx_logits(session: onnxruntime.InferenceSession, text: str) -> np.ndarray:
        batch = tokenizer(text, truncation=True, max_length=128, return_tensors="np")
        feeds = {i.name: batch[i.name].astype(np.int64) for i in session.get_inputs()}
        return session.run(None, feeds)[0][0]

    fp32_session = onnxruntime.InferenceSession(str(fp32_path), providers=["CPUExecutionProvider"])
    int8_session = onnxruntime.InferenceSession(str(int8_path), providers=["CPUExecutionProvider"])

    fp32_drift = 0.0
    int8_drift = 0.0
    for sentence in PARITY_SENTENCES:
        expected = torch_logits(sentence)
        fp32_drift = max(fp32_drift, float(np.abs(expected - onnx_logits(fp32_session, sentence)).max()))
        int8_drift = max(int8_drift, float(np.abs(expected - onnx_logits(int8_session, sentence)).max()))

    if fp32_drift > PARITY_TOLERANCE:
        raise RuntimeError(f"fp32 ONNX drifts {fp32_drift} from the torch reference, above {PARITY_TOLERANCE}")

    # The tokenizer.json next to the model is the artifact the Go scorer loads.
    report = {
        "temperature_folded": temperature,
        "opset": cfg.opset,
        "fp32_model": str(fp32_path),
        "int8_model": str(int8_path),
        "tokenizer": str(onnx_dir / "tokenizer.json"),
        "fp32_max_logit_drift": fp32_drift,
        "int8_max_logit_drift": int8_drift,
        "fp32_bytes": fp32_path.stat().st_size,
        "int8_bytes": int8_path.stat().st_size,
    }
    (cfg.out_dir / "export_report.json").write_text(json.dumps(report, indent=2))
    return report
