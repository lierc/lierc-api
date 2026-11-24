-- Written for the schema before 0011 (connection_id, user_id and channel
-- on logs and log_archives): already run in production, kept as a record.
--
-- One-off, after migration 5 is deployed: copy into highlights the
-- archive's highlights, and any in logs that the old logger wrote after
-- the migration's own copy (until the new logger replaced it). It reads
-- every archive block, about a minute in production, and can run while
-- everything is up. Highlights already there are left alone, so it's
-- safe to run again.
--
--   lierc-psql -f backfill-archived-highlights.sql
--
-- An archive entry keeps the highlight flag the logger set; self isn't
-- stored there, so it comes from the message's Prefix.Self, as the logger
-- sets it.
INSERT INTO highlights (id, connection_id, user_id, channel, time, message, self)
SELECT id, connection_id, user_id, channel, time, message, self FROM logs WHERE highlight
ON CONFLICT (id) DO NOTHING;

INSERT INTO highlights (id, connection_id, user_id, channel, time, message, self)
SELECT (e->>'id')::integer, a.connection_id, a.user_id, a.channel,
  to_timestamp((e->>'time')::bigint), (e->'message')::jsonb,
  coalesce((e#>>'{message,Prefix,Self}')::boolean, false)
FROM log_archives a, json_array_elements(a.messages) e
WHERE (e->>'highlight')::boolean
ON CONFLICT (id) DO NOTHING;
