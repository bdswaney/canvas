BEGIN;

DROP INDEX IF EXISTS docs_project_source_key_idx;
ALTER TABLE docs DROP COLUMN IF EXISTS source_key;

COMMIT;
