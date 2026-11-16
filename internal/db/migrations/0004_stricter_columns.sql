-- The columns state what the code already assumes: times are instants
-- (they were UTC wall-clock times; with TimeZone UTC the type change
-- rewrites no table, but the two indexes on a changed time column,
-- logs_time_idx and the archive's min_time index, are rebuilt: under a
-- second at production's sizes), a row's user is its connection's
-- owner, liercd is 'main' or 'next', channel names have no ASCII capitals
-- (the logger and the API lower-case them; only ASCII is checked, since
-- Go's and PostgreSQL's Unicode lower-casing can disagree), and highlight
-- is true or false. logs_sender, a GIN index the sender search's ->> = $n
-- can't use, goes.

SET LOCAL TimeZone = 'UTC';

ALTER TABLE users
  ALTER COLUMN last_login TYPE timestamptz,
  ALTER COLUMN registered TYPE timestamptz,
  ALTER COLUMN verify_sent TYPE timestamptz;
ALTER TABLE connections
  ALTER COLUMN created TYPE timestamptz,
  ALTER COLUMN updated TYPE timestamptz;
ALTER TABLE logs
  ALTER COLUMN time TYPE timestamptz,
  ALTER COLUMN channel TYPE text;
ALTER TABLE log_archives
  ALTER COLUMN min_time TYPE timestamptz,
  ALTER COLUMN max_time TYPE timestamptz,
  ALTER COLUMN channel TYPE text;
ALTER TABLE private_chats
  ALTER COLUMN time TYPE timestamptz;
ALTER TABLE seen_markers
  ALTER COLUMN updated TYPE timestamptz,
  ALTER COLUMN channel TYPE text;

-- a row's user is its connection's owner (connections never change owner)
ALTER TABLE connections ADD CONSTRAINT connections_id_user_id_key UNIQUE (id, user_id);
ALTER TABLE logs ADD CONSTRAINT logs_connection_id_user_id_fkey
  FOREIGN KEY (connection_id, user_id) REFERENCES connections (id, user_id) ON DELETE CASCADE;
ALTER TABLE log_archives ADD CONSTRAINT log_archives_connection_id_user_id_fkey
  FOREIGN KEY (connection_id, user_id) REFERENCES connections (id, user_id) ON DELETE CASCADE;
ALTER TABLE seen_markers ADD CONSTRAINT seen_markers_connection_id_user_id_fkey
  FOREIGN KEY (connection_id, user_id) REFERENCES connections (id, user_id) ON DELETE CASCADE;

UPDATE connections SET liercd = 'main' WHERE liercd IS NULL OR liercd = '';
ALTER TABLE connections
  ALTER COLUMN liercd SET NOT NULL,
  ALTER COLUMN liercd SET DEFAULT 'next',
  ADD CONSTRAINT connections_liercd_check CHECK (liercd IN ('main', 'next'));

ALTER TABLE logs ADD CONSTRAINT logs_channel_check CHECK (channel !~ '[A-Z]');
ALTER TABLE log_archives ADD CONSTRAINT log_archives_channel_check CHECK (channel !~ '[A-Z]');
ALTER TABLE seen_markers ADD CONSTRAINT seen_markers_channel_check CHECK (channel !~ '[A-Z]');

UPDATE logs SET highlight = false WHERE highlight IS NULL;
ALTER TABLE logs ALTER COLUMN highlight SET NOT NULL;

DROP INDEX logs_sender;
