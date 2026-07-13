-- The /tv page's recordings strip lists one channel's archived hours newest
-- first (ListTVRecordingsByChannel: WHERE kind='tv' AND channel_id=$1 AND
-- status='ready' AND recorded_at IS NOT NULL ORDER BY recorded_at DESC). Without
-- an index that is a full scan of videos plus an in-memory sort, and the videos
-- table grows one row per captured hour per channel. This partial index (tv rows
-- only, so it stays small and never weighs on the upload/import hot path) serves
-- both the channel_id equality and the recorded_at ordering.
CREATE INDEX IF NOT EXISTS videos_tv_channel_recorded_at_idx
    ON videos (channel_id, recorded_at DESC)
    WHERE kind = 'tv';
