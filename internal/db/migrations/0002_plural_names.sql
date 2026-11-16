-- Plural table names, and reference columns named for what they hold
-- ("user" -> user_id, connection -> connection_id), so nothing needs
-- quoting. Sequences, indexes and constraints follow. Renames only change
-- the catalog, but lock each table; the logger is stopped across the
-- deploy, since its old queries can't run against the new names.

ALTER TABLE "user"      RENAME TO users;
ALTER TABLE connection  RENAME TO connections;
ALTER TABLE login       RENAME TO logins;
ALTER TABLE pref        RENAME TO prefs;
ALTER TABLE log         RENAME TO logs;
ALTER TABLE log_archive RENAME TO log_archives;
ALTER TABLE web_push    RENAME TO web_push_subscriptions;
ALTER TABLE seen        RENAME TO seen_markers;
ALTER TABLE private     RENAME TO private_chats;

ALTER TABLE connections            RENAME COLUMN "user" TO user_id;
ALTER TABLE logins                 RENAME COLUMN "user" TO user_id;
ALTER TABLE prefs                  RENAME COLUMN "user" TO user_id;
ALTER TABLE logs                   RENAME COLUMN "user" TO user_id;
ALTER TABLE logs                   RENAME COLUMN connection TO connection_id;
ALTER TABLE log_archives           RENAME COLUMN "user" TO user_id;
ALTER TABLE log_archives           RENAME COLUMN connection TO connection_id;
ALTER TABLE web_push_subscriptions RENAME COLUMN "user" TO user_id;
ALTER TABLE seen_markers           RENAME COLUMN "user" TO user_id;
ALTER TABLE seen_markers           RENAME COLUMN connection TO connection_id;
ALTER TABLE private_chats          RENAME COLUMN connection TO connection_id;

ALTER SEQUENCE log_id_seq         RENAME TO logs_id_seq;
ALTER SEQUENCE log_archive_id_seq RENAME TO log_archives_id_seq;
ALTER SEQUENCE login_id_seq       RENAME TO logins_id_seq; -- logins.id's identity

-- Primary keys, unique, check and foreign key constraints (renaming a
-- primary key or unique constraint renames its index too).
ALTER TABLE users                  RENAME CONSTRAINT user_pkey TO users_pkey;
ALTER TABLE users                  RENAME CONSTRAINT user_username_valid TO users_username_valid;
ALTER TABLE connections            RENAME CONSTRAINT connection_pkey TO connections_pkey;
ALTER TABLE logins                 RENAME CONSTRAINT login_pkey TO logins_pkey;
ALTER TABLE logins                 RENAME CONSTRAINT login_token_hash_key TO logins_token_hash_key;
ALTER TABLE logins                 RENAME CONSTRAINT login_user_fkey TO logins_user_id_fkey;
ALTER TABLE prefs                  RENAME CONSTRAINT pref_pkey TO prefs_pkey;
ALTER TABLE logs                   RENAME CONSTRAINT log_pkey TO logs_pkey;
ALTER TABLE log_archives           RENAME CONSTRAINT log_archive_pkey TO log_archives_pkey;
ALTER TABLE web_push_subscriptions RENAME CONSTRAINT web_push_pkey TO web_push_subscriptions_pkey;
ALTER TABLE seen_markers           RENAME CONSTRAINT seen_pkey TO seen_markers_pkey;
ALTER TABLE private_chats          RENAME CONSTRAINT private_pkey TO private_chats_pkey;

ALTER INDEX user_email_lower_key    RENAME TO users_email_lower_key;
ALTER INDEX user_username_lower_key RENAME TO users_username_lower_key;
ALTER INDEX connection_user_idx     RENAME TO connections_user_id_idx;
ALTER INDEX login_user              RENAME TO logins_user_id_idx;
ALTER INDEX log_connection_channel_id_idx RENAME TO logs_connection_id_channel_id_idx;
ALTER INDEX log_time_idx            RENAME TO logs_time_idx;
ALTER INDEX log_message_text        RENAME TO logs_message_text;
ALTER INDEX log_sender              RENAME TO logs_sender;
ALTER INDEX log_user_highlight_idx  RENAME TO logs_user_id_highlight_idx;
ALTER INDEX log_archive_connection_channel_last_id_idx RENAME TO log_archives_connection_id_channel_last_id_idx;
ALTER INDEX log_archive_connection_channel_min_time_idx RENAME TO log_archives_connection_id_channel_min_time_idx;

-- PostgreSQL 18 names NOT NULL constraints <table>_<column>_not_null and
-- keeps the name through renames; 17 and older don't record them, so this
-- renames nothing there.
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT c.conrelid::regclass AS tbl, c.conname,
           format('%s_%s_not_null', t.relname, a.attname) AS want
      FROM pg_constraint c
      JOIN pg_class t ON t.oid = c.conrelid
      JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
     WHERE c.contype = 'n' AND c.connamespace = 'public'::regnamespace
  LOOP
    IF r.conname <> r.want THEN
      EXECUTE format('ALTER TABLE %s RENAME CONSTRAINT %I TO %I', r.tbl, r.conname, r.want);
    END IF;
  END LOOP;
END
$$;
