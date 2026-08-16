"""Dataset build for the check-worthiness classifier.

Positives are claims a professional fact-checker chose to check (ClaimReview
rows ingested into political_claims) plus a hand-annotated spoken-register
set. Negatives are real transcript statements the pipeline gate skipped plus a
hand-annotated opinion/hedge set written to survive the deterministic
heuristic, because only heuristic survivors ever reach this classifier at
runtime. The golden set never touches training: any row whose normalized form
collides with a golden row is dropped before the split.

Everything is deterministic under a fixed seed so a rebuild from the same
database state reproduces the same files byte for byte.
"""

from __future__ import annotations

import json
import random
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

from .textnorm import clean_statement, dedup_key

LABEL_CLAIM = 1
LABEL_NOT_CLAIM = 0

# Statements shorter than this are the heuristic's job, not the model's.
MIN_WORDS = 4
# ClaimReview texts are short by construction; anything longer is scraping
# noise, not a statement.
MAX_CHARS = 400


@dataclass(frozen=True)
class Example:
    text: str
    label: int
    source: str


def load_jsonl(path: Path, label: int, source: str) -> list[Example]:
    """Load one committed fixture: one JSON object with a "text" key per line."""
    examples: list[Example] = []
    with path.open(encoding="utf-8") as fh:
        for line_no, line in enumerate(fh, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            text = clean_statement(row["text"])
            if not text:
                raise ValueError(f"{path}:{line_no}: empty text")
            examples.append(Example(text=text, label=label, source=source))
    return examples


def load_golden(path: Path) -> list[Example]:
    """Load the golden holdout: rows carry their own "label" (claim/not_claim)."""
    examples: list[Example] = []
    with path.open(encoding="utf-8") as fh:
        for line_no, line in enumerate(fh, start=1):
            line = line.strip()
            if not line:
                continue
            row = json.loads(line)
            text = clean_statement(row["text"])
            raw_label = row["label"]
            if raw_label not in ("claim", "not_claim"):
                raise ValueError(f"{path}:{line_no}: unknown label {raw_label!r}")
            label = LABEL_CLAIM if raw_label == "claim" else LABEL_NOT_CLAIM
            examples.append(Example(text=text, label=label, source="golden"))
    return examples


def usable(text: str) -> bool:
    """Keep statements the runtime cascade could actually hand the model."""
    if len(text) > MAX_CHARS:
        return False
    return len(text.split()) >= MIN_WORDS


def merge(pools: Iterable[list[Example]], golden: list[Example]) -> list[Example]:
    """Concatenate pools, drop unusable rows, dedup, and shield the golden set.

    First occurrence wins on duplicate text, so pool order encodes source
    priority. Any row colliding with a golden row is dropped outright.
    """
    golden_keys = {dedup_key(g.text) for g in golden}
    seen: set[str] = set()
    merged: list[Example] = []
    for pool in pools:
        for ex in pool:
            if not usable(ex.text):
                continue
            key = dedup_key(ex.text)
            if not key or key in seen or key in golden_keys:
                continue
            seen.add(key)
            merged.append(ex)
    return merged


def balance(examples: list[Example], max_ratio: float, seed: int) -> list[Example]:
    """Cap the majority class at max_ratio times the minority class.

    The subsample is deterministic under seed and keeps every minority row, so
    a corpus with thousands of ClaimReview positives and hundreds of
    transcript negatives trains on a bounded imbalance instead of drowning the
    negatives.
    """
    positives = [e for e in examples if e.label == LABEL_CLAIM]
    negatives = [e for e in examples if e.label == LABEL_NOT_CLAIM]
    minority, majority = sorted((positives, negatives), key=len)
    cap = max(1, int(len(minority) * max_ratio))
    if len(majority) > cap:
        rng = random.Random(seed)
        majority = rng.sample(majority, cap)
    kept = {id(e) for e in minority} | {id(e) for e in majority}
    return [e for e in examples if id(e) in kept]


def split(examples: list[Example], val_fraction: float, seed: int) -> tuple[list[Example], list[Example]]:
    """Deterministic stratified train/validation split."""
    if not 0 < val_fraction < 1:
        raise ValueError(f"val_fraction must be in (0, 1), got {val_fraction}")
    rng = random.Random(seed)
    train: list[Example] = []
    val: list[Example] = []
    for label in (LABEL_NOT_CLAIM, LABEL_CLAIM):
        rows = [e for e in examples if e.label == label]
        rng.shuffle(rows)
        n_val = max(1, round(len(rows) * val_fraction)) if rows else 0
        val.extend(rows[:n_val])
        train.extend(rows[n_val:])
    rng.shuffle(train)
    rng.shuffle(val)
    return train, val


def write_jsonl(path: Path, examples: list[Example]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fh:
        for ex in examples:
            fh.write(json.dumps({"text": ex.text, "label": ex.label, "source": ex.source}, ensure_ascii=False) + "\n")


def load_postgres_pools(dsn: str) -> dict[str, list[Example]]:
    """Read the bootstrap pools from the platform database.

    - political_claims.content: ClaimReview-derived positives (a professional
      fact-checker chose to check each one).
    - claim_checks: live-gate telemetry; claim_text rows are positives the
      pipeline verified, gate-skip not_a_claim rows are negatives.
    - document_sentences with skip_reason='not_a_claim': batch-path negatives.
    - video_analyses.events: replayed live events; segment text labeled by the
      recorded skip_reason.
    """
    import psycopg

    pools: dict[str, list[Example]] = {
        "claimreview": [],
        "telemetry_claims": [],
        "telemetry_skips": [],
        "document_skips": [],
        "transcript_claims": [],
        "transcript_skips": [],
    }
    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute("SELECT content FROM political_claims")
        for (content,) in cur.fetchall():
            pools["claimreview"].append(Example(clean_statement(content), LABEL_CLAIM, "claimreview"))

        cur.execute(
            """
            SELECT unit_text, claim_text, decision_path, skip_reason
            FROM claim_checks
            WHERE locale = 'fr' OR locale = ''
            """
        )
        for unit_text, claim_text, decision_path, skip_reason in cur.fetchall():
            if claim_text:
                pools["telemetry_claims"].append(Example(clean_statement(claim_text), LABEL_CLAIM, "telemetry"))
            if decision_path == "gate-skip" and skip_reason == "not_a_claim" and unit_text:
                pools["telemetry_skips"].append(Example(clean_statement(unit_text), LABEL_NOT_CLAIM, "telemetry"))

        cur.execute("SELECT text FROM document_sentences WHERE skip_reason = 'not_a_claim'")
        for (text,) in cur.fetchall():
            pools["document_skips"].append(Example(clean_statement(text), LABEL_NOT_CLAIM, "document"))

        cur.execute("SELECT events FROM video_analyses")
        for (events,) in cur.fetchall():
            for claimed, skipped in [extract_transcript_examples(events)]:
                pools["transcript_claims"].extend(claimed)
                pools["transcript_skips"].extend(skipped)
    return pools


def extract_transcript_examples(events: object) -> tuple[list[Example], list[Example]]:
    """Mine a stored analysis event stream for labeled transcript statements.

    Events are the persisted []service.LiveEvent JSON. A segment carried by a
    skip event with reason not_a_claim is a negative; a segment that produced
    claims is a positive.
    """
    claims: list[Example] = []
    skips: list[Example] = []
    if not isinstance(events, list):
        return claims, skips
    for event in events:
        if not isinstance(event, dict):
            continue
        # LiveEvent serializes with Go's default field names (no json tags).
        segment = event.get("Segment") or {}
        text = clean_statement(str(segment.get("Text") or ""))
        if not text:
            continue
        skip_reason = event.get("SkipReason") or ""
        if skip_reason == "not_a_claim":
            skips.append(Example(text, LABEL_NOT_CLAIM, "transcript"))
        elif event.get("Claims"):
            claims.append(Example(text, LABEL_CLAIM, "transcript"))
    return claims, skips
