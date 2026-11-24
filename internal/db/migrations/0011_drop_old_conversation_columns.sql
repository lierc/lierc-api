-- Step 2 of channel_id everywhere (docs: 2026-11-24 channel_id
-- everywhere): logs, log_archives, highlights and seen_markers name their
-- conversation by channel_id alone. user_id, connection_id and channel go,
-- and with them their indexes, checks and foreign keys (seen_markers'
-- primary key among them); a row reaches its connection through channels
-- and its owner through connections. Irreversible.

-- Every lock up front, in the order the logger before this one takes them,
-- as 0010 does. Dropping the old foreign keys also locks connections and
-- users; nothing else holds those during the deploy (lierc-api is
-- restarting, ingest is stopped).
LOCK TABLE logs, highlights, channels, message_ids, log_archives, seen_markers IN ACCESS EXCLUSIVE MODE;

-- Rows the image before 0010 wrote after it (a rollback, then this): their
-- channel_id is empty, and their conversation may have no channels row.
INSERT INTO channels (connection_id, channel)
SELECT DISTINCT connection_id, channel FROM (
  SELECT connection_id, channel FROM logs WHERE channel_id IS NULL
  UNION SELECT connection_id, channel FROM highlights WHERE channel_id IS NULL
  UNION SELECT connection_id, channel FROM log_archives WHERE channel_id IS NULL
  UNION SELECT connection_id, channel FROM seen_markers WHERE channel_id IS NULL
) pairs
ORDER BY connection_id, channel
ON CONFLICT DO NOTHING;

UPDATE logs x SET channel_id = c.id FROM channels c
WHERE x.channel_id IS NULL AND c.connection_id = x.connection_id AND c.channel = x.channel;
UPDATE highlights x SET channel_id = c.id FROM channels c
WHERE x.channel_id IS NULL AND c.connection_id = x.connection_id AND c.channel = x.channel;
UPDATE log_archives x SET channel_id = c.id FROM channels c
WHERE x.channel_id IS NULL AND c.connection_id = x.connection_id AND c.channel = x.channel;
UPDATE seen_markers x SET channel_id = c.id FROM channels c
WHERE x.channel_id IS NULL AND c.connection_id = x.connection_id AND c.channel = x.channel;

ALTER TABLE logs
  ALTER COLUMN channel_id SET NOT NULL,
  DROP COLUMN user_id, DROP COLUMN connection_id, DROP COLUMN channel;
ALTER TABLE highlights
  ALTER COLUMN channel_id SET NOT NULL,
  DROP COLUMN user_id, DROP COLUMN connection_id, DROP COLUMN channel;
ALTER TABLE log_archives
  ALTER COLUMN channel_id SET NOT NULL,
  DROP COLUMN user_id, DROP COLUMN connection_id, DROP COLUMN channel;
ALTER TABLE seen_markers
  ALTER COLUMN channel_id SET NOT NULL,
  DROP COLUMN user_id, DROP COLUMN connection_id, DROP COLUMN channel;
-- a channel has one owner, so one marker
ALTER TABLE seen_markers ADD CONSTRAINT seen_markers_pkey PRIMARY KEY USING INDEX seen_markers_channel_id_key;
