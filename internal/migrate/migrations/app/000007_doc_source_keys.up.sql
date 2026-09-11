-- External source keys let MCP importers update the same document on repeat
-- runs without relying on a mutable display name.
BEGIN;

ALTER TABLE docs ADD COLUMN source_key text;

CREATE UNIQUE INDEX docs_project_source_key_idx
    ON docs (project_id, source_key)
    WHERE source_key IS NOT NULL AND deleted_at IS NULL;

COMMIT;
