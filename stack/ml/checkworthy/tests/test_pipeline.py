"""Unit tests for the pure logic of the training pipeline.

These run without torch, a database, or the network, so CI exercises the
dataset contract (dedup, golden shielding, balance, deterministic split) and
the calibration math on every change.
"""

import math
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from checkworthy_gate.calibrate import expected_calibration_error, fit_temperature, nll
from checkworthy_gate.dataset import (
    LABEL_CLAIM,
    LABEL_NOT_CLAIM,
    Example,
    balance,
    extract_transcript_examples,
    merge,
    split,
    usable,
)
from checkworthy_gate.textnorm import clean_statement, dedup_key


class TestTextNorm(unittest.TestCase):
    def test_dedup_key_folds_case_accents_punctuation(self):
        a = dedup_key("Le chômage a baissé, c'est un fait !")
        b = dedup_key("le chomage a baisse c est un fait")
        self.assertEqual(a, b)

    def test_dedup_key_distinguishes_different_statements(self):
        self.assertNotEqual(dedup_key("Le chômage baisse."), dedup_key("Le chômage monte."))

    def test_clean_statement_collapses_whitespace(self):
        self.assertEqual(clean_statement("  un\n texte\t propre "), "un texte propre")


class TestMerge(unittest.TestCase):
    def test_dedup_first_occurrence_wins(self):
        first = [Example("Le déficit dépasse cinq pour cent du PIB.", LABEL_CLAIM, "a")]
        second = [Example("Le déficit dépasse cinq pour cent du PIB !", LABEL_NOT_CLAIM, "b")]
        merged = merge([first, second], golden=[])
        self.assertEqual(len(merged), 1)
        self.assertEqual(merged[0].source, "a")

    def test_golden_rows_are_shielded_from_training(self):
        golden = [Example("La dette dépasse trois mille milliards d'euros.", LABEL_CLAIM, "golden")]
        pool = [
            Example("La dette dépasse trois mille milliards d'euros.", LABEL_CLAIM, "claimreview"),
            Example("Le budget de la défense atteint quarante-sept milliards.", LABEL_CLAIM, "claimreview"),
        ]
        merged = merge([pool], golden)
        self.assertEqual(len(merged), 1)
        self.assertNotEqual(dedup_key(merged[0].text), dedup_key(golden[0].text))

    def test_unusable_rows_dropped(self):
        pool = [
            Example("Trop court ici.", LABEL_CLAIM, "a"),
            Example("x" * 500, LABEL_CLAIM, "a"),
            Example("Une phrase assez longue pour être utilisable.", LABEL_CLAIM, "a"),
        ]
        merged = merge([pool], golden=[])
        self.assertEqual(len(merged), 1)

    def test_usable_boundaries(self):
        self.assertFalse(usable("un deux trois"))
        self.assertTrue(usable("un deux trois quatre"))


class TestBalance(unittest.TestCase):
    def _pool(self, n_pos, n_neg):
        pos = [Example(f"claim {i} avec assez de mots", LABEL_CLAIM, "p") for i in range(n_pos)]
        neg = [Example(f"opinion {i} avec assez de mots", LABEL_NOT_CLAIM, "n") for i in range(n_neg)]
        return pos + neg

    def test_majority_capped_at_ratio(self):
        balanced = balance(self._pool(1000, 100), max_ratio=3.0, seed=42)
        positives = sum(1 for e in balanced if e.label == LABEL_CLAIM)
        negatives = sum(1 for e in balanced if e.label == LABEL_NOT_CLAIM)
        self.assertEqual(negatives, 100)
        self.assertEqual(positives, 300)

    def test_minority_never_dropped(self):
        balanced = balance(self._pool(50, 400), max_ratio=2.0, seed=42)
        positives = sum(1 for e in balanced if e.label == LABEL_CLAIM)
        negatives = sum(1 for e in balanced if e.label == LABEL_NOT_CLAIM)
        self.assertEqual(positives, 50)
        self.assertEqual(negatives, 100)

    def test_deterministic_under_seed(self):
        a = balance(self._pool(500, 100), max_ratio=2.0, seed=7)
        b = balance(self._pool(500, 100), max_ratio=2.0, seed=7)
        self.assertEqual([e.text for e in a], [e.text for e in b])


class TestSplit(unittest.TestCase):
    def _pool(self):
        pos = [Example(f"claim numéro {i} suffisamment long", LABEL_CLAIM, "p") for i in range(90)]
        neg = [Example(f"opinion numéro {i} suffisamment longue", LABEL_NOT_CLAIM, "n") for i in range(30)]
        return pos + neg

    def test_split_is_deterministic_and_disjoint(self):
        train_a, val_a = split(self._pool(), val_fraction=0.1, seed=42)
        train_b, val_b = split(self._pool(), val_fraction=0.1, seed=42)
        self.assertEqual([e.text for e in train_a], [e.text for e in train_b])
        self.assertEqual([e.text for e in val_a], [e.text for e in val_b])
        overlap = {e.text for e in train_a} & {e.text for e in val_a}
        self.assertEqual(overlap, set())

    def test_split_is_stratified(self):
        train, val = split(self._pool(), val_fraction=0.1, seed=42)
        self.assertEqual(sum(1 for e in val if e.label == LABEL_CLAIM), 9)
        self.assertEqual(sum(1 for e in val if e.label == LABEL_NOT_CLAIM), 3)
        self.assertEqual(len(train) + len(val), 120)

    def test_rejects_degenerate_fraction(self):
        with self.assertRaises(ValueError):
            split(self._pool(), val_fraction=0.0, seed=42)


class TestTranscriptMining(unittest.TestCase):
    def test_labels_follow_recorded_events(self):
        events = [
            {"Segment": {"Text": "Le chômage a baissé de deux points."}, "Claims": [{"ID": "c1"}]},
            {"Segment": {"Text": "Bonjour à tous et bienvenue."}, "SkipReason": "not_a_claim"},
            {"Segment": {"Text": "Statement au raison inconnue."}, "SkipReason": "not_covered"},
            {"Segment": {"Text": ""}, "SkipReason": "not_a_claim"},
            "not-a-dict",
        ]
        claims, skips = extract_transcript_examples(events)
        self.assertEqual([e.text for e in claims], ["Le chômage a baissé de deux points."])
        self.assertEqual([e.text for e in skips], ["Bonjour à tous et bienvenue."])

    def test_non_list_events_yield_nothing(self):
        claims, skips = extract_transcript_examples({"segment": {}})
        self.assertEqual(claims, [])
        self.assertEqual(skips, [])


class TestCalibration(unittest.TestCase):
    def _synthetic(self, scale):
        # Overconfident model: every example gets the same strong positive
        # logits, but only 70 percent of labels are positive, so the optimal
        # temperature must soften the prediction toward 0.7.
        pairs = []
        labels = []
        for i in range(200):
            label = 1 if i % 10 < 7 else 0
            pairs.append((-scale / 2, scale / 2))
            labels.append(label)
        return pairs, labels

    def test_fit_recovers_overconfidence(self):
        pairs, labels = self._synthetic(scale=8.0)
        t = fit_temperature(pairs, labels)
        self.assertGreater(t, 2.0)
        self.assertLess(nll(pairs, labels, t), nll(pairs, labels, 1.0))

    def test_fit_leaves_calibrated_logits_alone(self):
        # Logit gap ln(0.7/0.3) exactly matches the 70/30 label mix, so the
        # fitted temperature should stay at one.
        gap = math.log(0.7 / 0.3)
        pairs = []
        labels = []
        for i in range(200):
            label = 1 if i % 10 < 7 else 0
            pairs.append((-gap / 2, gap / 2))
            labels.append(label)
        t = fit_temperature(pairs, labels)
        self.assertAlmostEqual(t, 1.0, delta=0.15)

    def test_ece_improves_after_temperature(self):
        pairs, labels = self._synthetic(scale=8.0)
        t = fit_temperature(pairs, labels)

        def prob(pair, temperature):
            zn, zp = pair[0] / temperature, pair[1] / temperature
            m = max(zn, zp)
            return math.exp(zp - m) / (math.exp(zn - m) + math.exp(zp - m))

        before = expected_calibration_error([prob(p, 1.0) for p in pairs], labels)
        after = expected_calibration_error([prob(p, t) for p in pairs], labels)
        self.assertLess(after, before)

    def test_nll_rejects_misaligned_inputs(self):
        with self.assertRaises(ValueError):
            nll([(0.0, 1.0)], [], 1.0)


if __name__ == "__main__":
    unittest.main()
