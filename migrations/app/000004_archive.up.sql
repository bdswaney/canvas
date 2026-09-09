-- Archiving instead of deleting. doc_versions is the durable layer of this
-- system — the whole point of the save design is that history is immutable —
-- and projects cascade to docs, which cascade to doc_versions, so a real
-- DELETE of a project would destroy the history under it. Setting a timestamp
-- hides the row from every read path and leaves the history intact.
--
-- There is no un-archive yet. Nothing is lost, so restoring one is an UPDATE
-- away, but it needs a decision about what it means to restore a document
-- whose project is still archived.
BEGIN;

ALTER TABLE projects ADD COLUMN deleted_at timestamptz;
ALTER TABLE docs ADD COLUMN deleted_at timestamptz;

COMMIT;
