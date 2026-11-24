-- Step 1 of channel_id everywhere (docs: 2026-11-24 channel_id
-- everywhere): logs, log_archives, highlights and seen_markers get a
-- channel_id naming their conversation's channels row, beside the old
-- user_id, connection_id and channel, which stay (and stay written) so the
-- image before this one still works: re-pinning it is a rollback. It
-- leaves channel_id empty, which is why channel_id is nullable until 0011
-- fills what it left and drops the old columns.

-- Every lock up front, in the order the logger before this one takes them
-- (logs, highlights, channels, message_ids), then the other two: a lock
-- taken later, in another order, could deadlock with a logger insert in
-- flight (ingest is stopped for the deploy; this is the guard).
LOCK TABLE logs, highlights, channels, message_ids, log_archives, seen_markers IN ACCESS EXCLUSIVE MODE;

-- every conversation the four tables mention gets its channels row
INSERT INTO channels (connection_id, channel)
SELECT DISTINCT connection_id, channel FROM (
  SELECT connection_id, channel FROM logs
  UNION SELECT connection_id, channel FROM highlights
  UNION SELECT connection_id, channel FROM log_archives
  UNION SELECT connection_id, channel FROM seen_markers
) pairs
ORDER BY connection_id, channel
ON CONFLICT DO NOTHING;

ALTER TABLE logs ADD COLUMN channel_id integer REFERENCES channels ON DELETE CASCADE;
UPDATE logs x SET channel_id = c.id FROM channels c
WHERE c.connection_id = x.connection_id AND c.channel = x.channel;
CREATE INDEX logs_channel_id_id_idx ON logs (channel_id, id DESC);

ALTER TABLE highlights ADD COLUMN channel_id integer REFERENCES channels ON DELETE CASCADE;
UPDATE highlights x SET channel_id = c.id FROM channels c
WHERE c.connection_id = x.connection_id AND c.channel = x.channel;
CREATE INDEX highlights_channel_id_id_idx ON highlights (channel_id, id DESC);

-- row by row: the TOASTed messages aren't rewritten
ALTER TABLE log_archives ADD COLUMN channel_id integer REFERENCES channels ON DELETE CASCADE;
UPDATE log_archives x SET channel_id = c.id FROM channels c
WHERE c.connection_id = x.connection_id AND c.channel = x.channel;
CREATE INDEX log_archives_channel_id_last_id_idx ON log_archives (channel_id, last_id);
CREATE INDEX log_archives_channel_id_min_time_idx ON log_archives (channel_id, min_time);

-- a channel has one owner, so one marker; 0011 makes this the primary key
ALTER TABLE seen_markers ADD COLUMN channel_id integer REFERENCES channels ON DELETE CASCADE;
UPDATE seen_markers x SET channel_id = c.id FROM channels c
WHERE c.connection_id = x.connection_id AND c.channel = x.channel;
CREATE UNIQUE INDEX seen_markers_channel_id_key ON seen_markers (channel_id);
