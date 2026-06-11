-- First-class video records: a durable identity for every playable clip,
-- independent of the bare filenames the demo route uses and of the SHA-256
-- processing identity in segment_results. A row maps one storage object
-- (object_key) to its lifecycle status. Uploads start pending and become ready
-- once their object is confirmed in storage; curated samples are seeded ready.
-- object_key is unique so seeding a sample is idempotent (ON CONFLICT
-- (object_key)). id uses gen_random_uuid(), built into Postgres 13+, so no
-- extension is required.
CREATE TABLE videos (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title        text NOT NULL,
    object_key   text NOT NULL UNIQUE,
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL,
    status       text NOT NULL,
    kind         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- The library lists newest first.
CREATE INDEX videos_created_at_idx ON videos (created_at DESC);
