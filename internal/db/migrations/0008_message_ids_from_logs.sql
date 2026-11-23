-- Messages logged before 0007 are mapped in message_ids too, for those
-- still in logs (archived ones can't be found by msgid either way), so
-- MessageByMsgid needs no fallback to logs and logs' msgid index can go.
-- About 35,000 rows in production. Safe to run again: only unmapped rows
-- are added, and the logger may be writing meanwhile (a duplicate row
-- would do no harm).

INSERT INTO channels (connection_id, channel)
SELECT DISTINCT connection_id, channel
FROM logs
WHERE command IN ('PRIVMSG', 'NOTICE') AND message->'Tags'->>'msgid' IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO message_ids (channel_id, msgid, log_id)
SELECT c.id, l.message->'Tags'->>'msgid', l.id
FROM logs l
JOIN channels c ON c.connection_id = l.connection_id AND c.channel = l.channel
WHERE l.command IN ('PRIVMSG', 'NOTICE')
  AND l.message->'Tags'->>'msgid' IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM message_ids m WHERE m.log_id = l.id);

DROP INDEX IF EXISTS logs_connection_id_msgid_idx;
