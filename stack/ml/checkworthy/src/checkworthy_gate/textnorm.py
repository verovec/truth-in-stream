"""Text normalization shared by dataset dedup and golden-set exclusion.

The dedup key intentionally mirrors the Go gate's folding (lowercase, strip
diacritics, collapse everything that is not a letter or digit) so a statement
cannot slip into training twice under cosmetic variation, and so no training
row can collide with a golden-set row that differs only in punctuation.
"""

from __future__ import annotations

import unicodedata


def dedup_key(text: str) -> str:
    """Return the canonical form used to detect duplicate statements."""
    decomposed = unicodedata.normalize("NFKD", text.lower())
    kept: list[str] = []
    prev_space = True
    for ch in decomposed:
        if unicodedata.combining(ch):
            continue
        if ch.isalnum():
            kept.append(ch)
            prev_space = False
        elif not prev_space:
            kept.append(" ")
            prev_space = True
    return "".join(kept).strip()


def clean_statement(text: str) -> str:
    """Collapse whitespace and strip control characters for a training row."""
    return " ".join(text.split())
