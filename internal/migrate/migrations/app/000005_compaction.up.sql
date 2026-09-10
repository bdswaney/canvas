-- Compaction replaces a run of journal rows with one merged update carrying
-- the same document, so a joining client replays one frame instead of
-- hundreds.
--
-- Rows are marked rather than deleted. This is the first operation in Canvas
-- that would destroy data, and a mistake here does not surface as an error —
-- it surfaces as a document that replays into different text for everyone who
-- joins afterwards. Marking makes that recoverable: setting superseded_at back
-- to NULL restores the original journal exactly. Deleting superseded rows is a
-- later, separate change, once this has run for a while.
BEGIN;

ALTER TABLE doc_updates ADD COLUMN superseded_at timestamptz;

-- Replay reads only live rows, so the index it uses has to select on that.
DROP INDEX IF EXISTS doc_updates_doc_id_idx;
CREATE INDEX doc_updates_live_idx ON doc_updates (doc_id, id) WHERE superseded_at IS NULL;

-- Finding what is worth compacting, and later what is safe to delete.
CREATE INDEX doc_updates_superseded_idx ON doc_updates (doc_id) WHERE superseded_at IS NOT NULL;

COMMIT;
