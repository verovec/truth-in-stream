-- Live TV channels the platform captures and fact-checks. A channel names a
-- free, non-DRM source (an official YouTube live or a parliamentary HLS
-- manifest) that the tvcapture worker resolves. The row is the single control
-- surface end to end: the worker reconciles running captures against the
-- enabled set, the /tv page renders the list, and the backoffice manages it.
-- slug is the stable, human-readable key used in storage paths and seeds, so it
-- is UNIQUE and reseeding is idempotent (ON CONFLICT (slug)). source_kind is
-- checked at the DB layer so an unknown capture strategy can never be stored.
-- enabled defaults false: a fresh registry captures nothing until the operator
-- deliberately turns a channel on. archive_enabled defaults true but only takes
-- effect once a channel is enabled; the per-channel opt-out keeps the archiving
-- posture (YouTube ToS, retention) a deliberate per-source decision.
CREATE TABLE tv_channels (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            text NOT NULL UNIQUE,
    name            text NOT NULL,
    source_kind     text NOT NULL CHECK (source_kind IN ('youtube', 'hls')),
    source_ref      text NOT NULL,
    enabled         boolean NOT NULL DEFAULT false,
    archive_enabled boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- A captured hour is archived as an ordinary videos row (kind 'tv'); channel_id
-- links it back to its source and recorded_at is the wall-clock start of the
-- segment. ON DELETE SET NULL keeps recordings when their channel is removed
-- (the FK nulls, the media and row stay), so deleting a channel never destroys
-- its archive. Both columns are NULL for uploads, samples, and YouTube imports.
ALTER TABLE videos ADD COLUMN channel_id  uuid REFERENCES tv_channels (id) ON DELETE SET NULL;
ALTER TABLE videos ADD COLUMN recorded_at timestamptz;
