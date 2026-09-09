-- Copied verbatim from github.com/cccteam/session v0.11.0
-- schema/postgresql/migrations/000002_SessionUsers.down.sql
-- Keep in sync when upgrading the session module. Applied with its own
-- migrations table (session_schema_migrations) so its numbering never
-- collides with the application's own migrations.

DROP TABLE "SessionUsers";