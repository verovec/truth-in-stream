-- Drop the batch fact-check result tables. AssemblyAI is now the only
-- transcriber and imported videos stream live exactly like a live stream, so
-- there is no batch transcription pipeline persisting per-segment results: the
-- live path emits verdicts over the WebSocket and stores nothing. These tables
-- (created in 0003, extended in 0005) have no remaining reader or writer.
DROP TABLE IF EXISTS processed_videos;
DROP TABLE IF EXISTS segment_results;
