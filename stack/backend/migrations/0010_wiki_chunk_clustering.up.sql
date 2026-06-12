-- Topic-clustering outputs for the wiki corpus: cluster_id groups a chunk into a
-- semantic cluster (and gives the confidence layer a notion of which evidence
-- cluster a chunk belongs to) and importance is a [0,1] score the ingestion
-- producer maps onto embedding-job priority, so the most important content
-- embeds first. Both are nullable and written by the offline clustering batch
-- job after a corpus is embedded; until it runs they are NULL and the producer
-- falls back to the kind heuristic. The columns are additive, so the runtime
-- staging table created with
-- `CREATE TABLE wiki_chunks_staging (LIKE wiki_chunks INCLUDING DEFAULTS)` gains
-- them and the atomic staging-to-live swap stays symmetric; a fresh swap leaves
-- them NULL again, so the job is re-run over each new live corpus.
ALTER TABLE wiki_chunks
    ADD COLUMN cluster_id integer,
    ADD COLUMN importance double precision;
