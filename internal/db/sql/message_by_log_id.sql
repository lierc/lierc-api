-- MessageByMsgid, then: the PRIVMSG or NOTICE with id $3 on connection $1
-- in channel $2, from logs, else from the one log_archives block of that
-- channel whose id range holds it (found by its connection, channel,
-- last_id index), unpacked.
SELECT id, message::text, connection_id, highlight
FROM logs
WHERE id = $3::int AND connection_id = $1 AND command IN ('PRIVMSG', 'NOTICE')
UNION ALL
SELECT (e->>'id')::int, (e->'message')::text, a.connection_id, (e->>'highlight')::bool
FROM log_archives a, json_array_elements(a.messages) e
WHERE a.connection_id = $1 AND a.channel = $2
  AND a.first_id <= $3::int AND a.last_id >= $3::int
  AND (e->>'id')::int = $3::int
  AND e->'message'->>'Command' IN ('PRIVMSG', 'NOTICE')
LIMIT 1
