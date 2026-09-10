-- Rolling back restores every superseded row to the journal. Replay stays
-- correct either way: Yjs updates are idempotent, so a document that has been
-- compacted simply replays its history twice.
BEGIN;

DROP INDEX IF EXISTS doc_updates_superseded_idx;
DROP INDEX IF EXISTS doc_updates_live_idx;
ALTER TABLE doc_updates DROP COLUMN superseded_at;

CREATE INDEX doc_updates_doc_id_idx ON doc_updates (doc_id, id);

COMMIT;
