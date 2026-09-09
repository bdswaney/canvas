BEGIN;

DROP TABLE IF EXISTS doc_updates;
DROP TABLE IF EXISTS doc_state;
DROP TABLE IF EXISTS doc_versions;
DROP TABLE IF EXISTS docs;
DROP TABLE IF EXISTS projects;

CREATE TABLE IF NOT EXISTS room_updates (
    id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    room   text NOT NULL,
    update bytea NOT NULL,
    added  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS room_updates_room_id_idx ON room_updates (room, id);

COMMIT;
