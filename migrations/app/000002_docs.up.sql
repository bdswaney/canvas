-- Docs become the durable unit: a doc belongs to a project, has an immutable
-- history of saved versions, and a disposable live state made of one snapshot
-- plus a journal of Yjs updates.
--
-- room_updates is dropped rather than migrated. It was keyed by a name taken
-- from the URL, with no doc to attach it to.
BEGIN;

DROP TABLE IF EXISTS room_updates;

CREATE TABLE projects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE docs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name            text NOT NULL,
    current_version integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX docs_project_idx ON docs (project_id, name);

-- History. People are referenced by SessionUsers.Id and never by username,
-- which is mutable: the session package changes it in place on rename.
CREATE TABLE doc_versions (
    doc_id          uuid NOT NULL REFERENCES docs (id) ON DELETE CASCADE,
    version         integer NOT NULL,
    artifact        text NOT NULL,
    artifact_sha256 bytea NOT NULL,
    author_id       uuid REFERENCES "SessionUsers" ("Id"),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (doc_id, version)
);

-- The CRDT state as of the last save. Disposable: it exists so a doc can be
-- restored, and later so the journal can be trimmed.
CREATE TABLE doc_state (
    doc_id          uuid PRIMARY KEY REFERENCES docs (id) ON DELETE CASCADE,
    snapshot        bytea NOT NULL,
    saved_version   integer NOT NULL,
    artifact_sha256 bytea NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- The update journal, replayed to every joining client. Nothing deletes from
-- it yet; compaction needs a protocol that can tell a client which rows it
-- has seen.
CREATE TABLE doc_updates (
    id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    doc_id uuid NOT NULL REFERENCES docs (id) ON DELETE CASCADE,
    update bytea NOT NULL,
    added  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX doc_updates_doc_id_idx ON doc_updates (doc_id, id);

-- Somewhere to put the first documents until projects get their own UI.
INSERT INTO projects (id, name)
VALUES ('00000000-0000-4000-8000-000000000001', 'Default');

COMMIT;
