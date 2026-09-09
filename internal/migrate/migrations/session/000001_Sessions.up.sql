-- Copied verbatim from github.com/cccteam/session v0.11.0
-- schema/postgresql/migrations/000001_Sessions.up.sql
-- Keep in sync when upgrading the session module. Applied with its own
-- migrations table (session_schema_migrations) so its numbering never
-- collides with the application's own migrations.

BEGIN;

-- Table: Sessions

-- DROP TABLE "Sessions";

CREATE TABLE "Sessions"
(
    "Id" UUID NOT NULL,
    "Username" character varying NOT NULL,
    "CreatedAt" timestamp without time zone NOT NULL,
    "UpdatedAt" timestamp without time zone NOT NULL,
    "Expired" boolean NOT NULL,
    CONSTRAINT "Sessions_pkey" PRIMARY KEY ("Id")
);

-- DROP INDEX "Sessions_Expired_idx";

CREATE INDEX "Sessions_Expired_idx"
    ON "Sessions" USING btree
    ("Expired" ASC NULLS LAST);

COMMIT;