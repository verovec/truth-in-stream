-- name: ListVideoSpeakers :many
SELECT video_id, label, display_name, role, confidence, method, evidence, operator_set, created_at, updated_at
FROM video_speakers
WHERE video_id = $1
ORDER BY label;

-- name: LockVideoForSpeakerNaming :one
-- Serialize the writers of one video's speaker mapping: a naming run holds the
-- video row for its whole replace, so two overlapping runs cannot interleave
-- their delete and insert steps and leave a mix of both.
SELECT id
FROM videos
WHERE id = $1
FOR UPDATE;

-- name: DeleteProposedVideoSpeakers :exec
-- Clear the naming step's previous proposals before a re-run writes new ones.
-- Rows an operator confirmed or overrode are kept.
DELETE FROM video_speakers
WHERE video_id = $1
  AND NOT operator_set;

-- name: InsertProposedVideoSpeaker :exec
-- Write one proposal of the naming step. The caller holds the video row lock
-- and has just deleted the step's own rows, so a conflict can only be an
-- operator-set row, and that row wins.
INSERT INTO video_speakers (video_id, label, display_name, role, confidence, method, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (video_id, label) DO NOTHING;

-- name: SetOperatorVideoSpeaker :one
-- Record an operator's decision for one label. The step's evidence is kept
-- beside the override so the operator still sees what the proposal rested on.
INSERT INTO video_speakers (video_id, label, display_name, role, confidence, method, operator_set)
VALUES ($1, $2, $3, $4, 1, 'operator', true)
ON CONFLICT (video_id, label) DO UPDATE
    SET display_name = EXCLUDED.display_name,
        role         = EXCLUDED.role,
        confidence   = 1,
        method       = 'operator',
        operator_set = true,
        updated_at   = now()
RETURNING video_id, label, display_name, role, confidence, method, evidence, operator_set, created_at, updated_at;
