"""Unit tests for the NLI stance pipeline's pure logic (no ONNX, no network)."""

import json
import math
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from checkworthy_gate.nli import (
    IDX_CONTRADICTION,
    IDX_ENTAILMENT,
    IDX_NEUTRAL,
    Case,
    decide,
    derive_pairs_from_golden,
    fit_temperature3,
    load_pair_fixture,
    negation_violations,
    nll3,
    softmax3,
)


def probs(entail=0.0, neutral=0.0, contradiction=0.0):
    row = [0.0, 0.0, 0.0]
    row[IDX_ENTAILMENT] = entail
    row[IDX_NEUTRAL] = neutral
    row[IDX_CONTRADICTION] = contradiction
    return row


class TestSoftmax3(unittest.TestCase):
    def test_sums_to_one_and_orders(self):
        p = softmax3([2.0, 0.5, -1.0], 1.0)
        self.assertAlmostEqual(sum(p), 1.0)
        self.assertGreater(p[0], p[1])
        self.assertGreater(p[1], p[2])

    def test_temperature_softens(self):
        sharp = softmax3([4.0, 0.0, 0.0], 1.0)
        soft = softmax3([4.0, 0.0, 0.0], 4.0)
        self.assertGreater(sharp[0], soft[0])

    def test_large_logits_stay_finite(self):
        p = softmax3([800.0, -800.0, 0.0], 1.0)
        self.assertFalse(any(math.isnan(v) or math.isinf(v) for v in p))


class TestTemperatureFit3(unittest.TestCase):
    def test_overconfident_logits_get_softened(self):
        # Every row confidently predicts entailment but only 60 percent of
        # labels agree; the fitted temperature must be well above one.
        logits = [[6.0, 0.0, 0.0]] * 100
        labels = [IDX_ENTAILMENT if i % 10 < 6 else IDX_NEUTRAL for i in range(100)]
        t = fit_temperature3(logits, labels)
        self.assertGreater(t, 2.0)
        self.assertLess(nll3(logits, labels, t), nll3(logits, labels, 1.0))

    def test_rejects_misaligned_inputs(self):
        with self.assertRaises(ValueError):
            nll3([[1.0, 0.0, 0.0]], [], 1.0)


class TestConsensusRule(unittest.TestCase):
    def test_clear_support(self):
        self.assertEqual(decide([probs(entail=0.9)], 0.7, 0.9, 1), "support")

    def test_clear_refute(self):
        self.assertEqual(decide([probs(contradiction=0.95)], 0.7, 0.9, 1), "refute")

    def test_all_neutral_escalates(self):
        self.assertEqual(decide([probs(neutral=0.9)], 0.7, 0.9, 1), "escalate")

    def test_mixed_signals_escalate(self):
        rows = [probs(entail=0.9), probs(contradiction=0.95)]
        self.assertEqual(decide(rows, 0.7, 0.9, 1), "escalate")

    def test_min_agree_enforced(self):
        rows = [probs(entail=0.9), probs(neutral=0.8)]
        self.assertEqual(decide(rows, 0.7, 0.9, 2), "escalate")
        rows = [probs(entail=0.9), probs(entail=0.85)]
        self.assertEqual(decide(rows, 0.7, 0.9, 2), "support")

    def test_below_threshold_escalates(self):
        self.assertEqual(decide([probs(entail=0.6)], 0.7, 0.9, 1), "escalate")
        self.assertEqual(decide([probs(contradiction=0.85)], 0.7, 0.9, 1), "escalate")


class TestNegationControl(unittest.TestCase):
    def _cases(self):
        a = Case(case_id="a", claim="c", label="support", pair_indices=[0])
        b = Case(case_id="b", claim="not c", label="refute", pair_indices=[1], negation_of="a")
        return [a, b]

    def test_opposite_stances_pass(self):
        rows = [probs(entail=0.9), probs(contradiction=0.95)]
        self.assertEqual(negation_violations(self._cases(), rows, 0.7, 0.9, 1), [])

    def test_same_stance_flags(self):
        rows = [probs(entail=0.9), probs(entail=0.9)]
        violations = negation_violations(self._cases(), rows, 0.7, 0.9, 1)
        self.assertEqual(len(violations), 1)

    def test_escalation_is_not_a_violation(self):
        rows = [probs(neutral=0.9), probs(neutral=0.9)]
        self.assertEqual(negation_violations(self._cases(), rows, 0.7, 0.9, 1), [])


class TestPairDerivation(unittest.TestCase):
    def test_golden_mapping(self):
        doc = {
            "cases": [
                {
                    "id": "ok",
                    "statement": "claim accurate",
                    "expected_literal": "accurate",
                    "passages": [{"id": "p1", "text": "evidence"}, {"id": "p2", "text": "distractor"}],
                    "relevant": ["p1"],
                },
                {
                    "id": "bad",
                    "statement": "claim inaccurate",
                    "expected_literal": "inaccurate",
                    "passages": [{"id": "p3", "text": "refuting"}],
                },
                {
                    "id": "unv",
                    "statement": "claim unverifiable",
                    "expected_literal": "unverifiable",
                    "passages": [{"id": "p4", "text": "unrelated"}],
                },
            ]
        }
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "golden.json"
            path.write_text(json.dumps(doc))
            pairs, cases = derive_pairs_from_golden(path)
        self.assertEqual([p.label for p in pairs], [IDX_ENTAILMENT, IDX_NEUTRAL, IDX_CONTRADICTION, IDX_NEUTRAL])
        self.assertEqual([c.label for c in cases], ["support", "refute", "neutral"])
        self.assertEqual(cases[0].pair_indices, [0, 1])

    def test_fixture_roundtrip_and_negation_link(self):
        rows = [
            {"id": "a", "premise": "p", "claim": "c", "label": "support"},
            {"id": "b", "premise": "p", "claim": "non c", "label": "refute", "negation_of": "a"},
        ]
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "pairs.jsonl"
            path.write_text("\n".join(json.dumps(r) for r in rows))
            pairs, cases = load_pair_fixture(path)
        self.assertEqual(len(pairs), 2)
        self.assertEqual(cases[1].negation_of, "a")
        self.assertEqual(pairs[0].label, IDX_ENTAILMENT)
        self.assertEqual(pairs[1].label, IDX_CONTRADICTION)

    def test_fixture_rejects_unknown_label(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "pairs.jsonl"
            path.write_text(json.dumps({"id": "x", "premise": "p", "claim": "c", "label": "maybe"}))
            with self.assertRaises(ValueError):
                load_pair_fixture(path)


if __name__ == "__main__":
    unittest.main()
