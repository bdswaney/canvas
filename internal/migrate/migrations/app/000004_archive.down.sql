BEGIN;

-- Rolling back discards the record of what was archived, so anything hidden
-- becomes visible again rather than being deleted.
ALTER TABLE docs DROP COLUMN deleted_at;
ALTER TABLE projects DROP COLUMN deleted_at;

COMMIT;
