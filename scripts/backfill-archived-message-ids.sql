-- Written for the schema before 0011 (connection_id, user_id and channel
-- on logs and log_archives): already run in production, kept as a record.
--
-- One-off, after migration 0007/0008 are deployed: map in message_ids the
-- PRIVMSGs and NOTICEs with a msgid in archive blocks from the last 90
-- days, archived before message_ids existed, so a reply to one of them
-- finds its original. Messages logged since 0007 are mapped as they're
-- logged and keep their rows when archived, so this only has to run once.
--
--   lierc-psql -f backfill-archived-message-ids.sql
--
-- In production (2026-11-23) the window is about 1,500 blocks, ~750,000
-- archived messages, ~310,000 rows (~37 MB): a minute or two. It can run
-- while everything is up, and is safe to run again: msgids already mapped
-- on a channel are skipped. To go further back, change both
-- "90 days" below.
INSERT INTO channels (connection_id, channel)
SELECT DISTINCT connection_id, channel
FROM log_archives
WHERE max_time > now() - interval '90 days'
ON CONFLICT DO NOTHING;

INSERT INTO message_ids (channel_id, msgid, log_id)
SELECT c.id, e->'message'->'Tags'->>'msgid', (e->>'id')::integer
FROM log_archives a
JOIN channels c ON c.connection_id = a.connection_id AND c.channel = a.channel
CROSS JOIN LATERAL json_array_elements(a.messages) e
WHERE a.max_time > now() - interval '90 days'
  AND e->'message'->>'Command' IN ('PRIVMSG', 'NOTICE')
  AND e->'message'->'Tags'->>'msgid' IS NOT NULL
  AND NOT EXISTS (
    SELECT 1 FROM message_ids m
    WHERE m.msgid = e->'message'->'Tags'->>'msgid' AND m.channel_id = c.id
  );
