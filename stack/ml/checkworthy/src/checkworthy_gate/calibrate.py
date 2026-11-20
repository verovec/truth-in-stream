"""Temperature scaling for the classifier head.

A single temperature T rescales the validation logits (logit / T) to minimize
negative log-likelihood, the standard post-hoc calibration for neural
classifiers (Guo et al. 2017). The export step folds 1/T into the final
linear layer's weights and bias - an exact transformation - so the shipped
ONNX graph emits calibrated logits and the Go side needs nothing beyond a
plain softmax.
"""

from __future__ import annotations

import math


def nll(logit_pairs: list[tuple[float, float]], labels: list[int], temperature: float) -> float:
    """Mean negative log-likelihood of the positive class under temperature."""
    if len(logit_pairs) != len(labels) or not labels:
        raise ValueError("logits and labels must be non-empty and aligned")
    total = 0.0
    for (neg, pos), label in zip(logit_pairs, labels):
        zn, zp = neg / temperature, pos / temperature
        m = max(zn, zp)
        log_denom = m + math.log(math.exp(zn - m) + math.exp(zp - m))
        log_p = (zp if label == 1 else zn) - log_denom
        total -= log_p
    return total / len(labels)


def fit_temperature(logit_pairs: list[tuple[float, float]], labels: list[int]) -> float:
    """Golden-section search for the NLL-minimizing temperature in [0.05, 10].

    The NLL is unimodal in T, so golden-section converges without gradients;
    that keeps calibration free of any framework dependency and byte-for-byte
    reproducible across platforms.
    """
    lo, hi = 0.05, 10.0
    inv_phi = (math.sqrt(5) - 1) / 2
    a, b = lo, hi
    c = b - inv_phi * (b - a)
    d = a + inv_phi * (b - a)
    fc = nll(logit_pairs, labels, c)
    fd = nll(logit_pairs, labels, d)
    for _ in range(200):
        if b - a < 1e-6:
            break
        if fc < fd:
            b, d, fd = d, c, fc
            c = b - inv_phi * (b - a)
            fc = nll(logit_pairs, labels, c)
        else:
            a, c, fc = c, d, fd
            d = a + inv_phi * (b - a)
            fd = nll(logit_pairs, labels, d)
    return (a + b) / 2


def expected_calibration_error(probabilities: list[float], labels: list[int], bins: int = 10) -> float:
    """ECE over equal-width confidence bins, for the calibration report."""
    if len(probabilities) != len(labels) or not labels:
        raise ValueError("probabilities and labels must be non-empty and aligned")
    totals = [0] * bins
    correct = [0.0] * bins
    confidence = [0.0] * bins
    for p, label in zip(probabilities, labels):
        predicted = 1 if p >= 0.5 else 0
        conf = p if predicted == 1 else 1 - p
        idx = min(bins - 1, int(conf * bins))
        totals[idx] += 1
        correct[idx] += 1.0 if predicted == label else 0.0
        confidence[idx] += conf
    ece = 0.0
    n = len(labels)
    for i in range(bins):
        if totals[i] == 0:
            continue
        ece += (totals[i] / n) * abs(correct[i] / totals[i] - confidence[i] / totals[i])
    return ece
