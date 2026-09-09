-- The Yjs update log. One row per update relayed for a room; replayed in id
-- order to a joining client. Compaction into saved versions comes later.
BEGIN;

CREATE TABLE IF NOT EXISTS room_updates (
    id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room   text NOT NULL,
    update bytea NOT NULL,
    added  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS room_updates_room_id_idx ON room_updates (room, id);

COMMIT;
