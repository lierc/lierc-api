-- Highlights get their own table, read by /highlight, so they outlive
-- their log rows when those are archived. The logger writes both rows,
-- with the same id, in one statement. Only logs' highlights are copied
-- here: the archive's take a scan of every block (about a minute in
-- production), which would hold up lierc-api's start, so they're copied
-- separately while it runs (scripts/backfill-archived-highlights.sql,
-- which also picks up any the old logger writes after this copy).
CREATE TABLE highlights (
  id            integer PRIMARY KEY,
  connection_id varchar(24) NOT NULL,
  user_id       varchar(24) NOT NULL REFERENCES users ON DELETE CASCADE,
  channel       text NOT NULL CHECK (channel !~ '[A-Z]'),
  time          timestamptz NOT NULL,
  message       jsonb NOT NULL,
  self          boolean NOT NULL DEFAULT false,
  FOREIGN KEY (connection_id, user_id) REFERENCES connections (id, user_id) ON DELETE CASCADE
);
CREATE INDEX highlights_user_id_id_idx ON highlights (user_id, id DESC);
CREATE INDEX highlights_connection_id_idx ON highlights (connection_id);

INSERT INTO highlights (id, connection_id, user_id, channel, time, message, self)
SELECT id, connection_id, user_id, channel, time, message, self FROM logs WHERE highlight;
