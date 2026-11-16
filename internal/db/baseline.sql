-- One-off, for production only: mark the lierc database, whose tables were
-- made by hand before lierc-api had migrations, as having had migration 1
-- (migrations/0001_initial.sql), so lierc-api doesn't try to create them.
--
-- It also drops the two columns production has and 0001 doesn't, which
-- nothing has used for years: "user".last_id and connection.enabled. After
-- it, production's schema is exactly 0001's (TestBaselineMatchesProduction
-- checks this against testdata/production-schema.sql, production's
-- pg_dump --schema-only from 2026-11-16).
--
-- Run it once, BEFORE the first deploy of a lierc-api with migrations:
--
--   lierc-psql -f baseline.sql
--
-- Without it, that lierc-api would try to create tables that exist, fail,
-- and stay down. It's one transaction, so running it twice fails
-- harmlessly (schema_migrations exists) and changes nothing.
-- TestProductionBaseline runs what follows the guard on a test database.

\set ON_ERROR_STOP on
DO $$
BEGIN
  IF current_database() <> 'lierc' OR current_user <> 'lierc' THEN
    RAISE EXCEPTION 'run this in database lierc as lierc (lierc-psql), not % as %',
      current_database(), current_user;
  END IF;
  IF to_regclass('public.log_archive') IS NULL THEN
    RAISE EXCEPTION 'no log_archive table: this database lacks the schema; let lierc-api create it instead';
  END IF;
END
$$;
-- end of guard
BEGIN;
ALTER TABLE "user" DROP COLUMN IF EXISTS last_id;
ALTER TABLE connection DROP COLUMN IF EXISTS enabled;
CREATE TABLE schema_migrations (
  version    int PRIMARY KEY,
  name       text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_migrations (version, name) VALUES (1, 'initial');
COMMIT;
