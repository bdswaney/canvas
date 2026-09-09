-- Who belongs to a project. Membership is the authorization boundary: a
-- document and a project are reachable only by members of the project the
-- document belongs to. docs.project_id is NOT NULL, so every document has
-- exactly one project and the gate is a single join.
BEGIN;

CREATE TABLE project_members (
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES "SessionUsers" ("Id") ON DELETE CASCADE,
    added_by   uuid REFERENCES "SessionUsers" ("Id"),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

-- Every list endpoint filters by the person asking, which is a lookup by user.
CREATE INDEX project_members_user_idx ON project_members (user_id);

-- Preserve what was true the moment before this migration: everyone could see
-- everything. Seeding every existing account into every existing project
-- means nobody loses access at the cut. It is a policy choice, not a neutral
-- one, and it applies only to rows that already exist: accounts and projects
-- created afterwards get memberships explicitly.
INSERT INTO project_members (project_id, user_id)
SELECT p.id, u."Id" FROM projects p CROSS JOIN "SessionUsers" u
ON CONFLICT DO NOTHING;

COMMIT;
