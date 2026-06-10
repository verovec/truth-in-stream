-- The check-worthiness gate (VER-24) records why a segment was not
-- fact-checked. An empty string means the segment was checked and its verdicts
-- live in matches; a non-empty reason ("not_a_claim", "not_covered") means the
-- segment was skipped and carries no verdict. A skip is categorically distinct
-- from a verdict, so it gets its own column rather than a sentinel inside
-- matches. Existing rows predate the gate and were all checked, so the default
-- empty string is correct for them.
ALTER TABLE segment_results
    ADD COLUMN skip_reason text NOT NULL DEFAULT '';
