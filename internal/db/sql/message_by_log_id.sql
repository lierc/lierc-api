-- MessageByMsgid, then: the PRIVMSG or NOTICE with id $3 on connection $1
-- in channel $2, from logs, else unpacked from the archive block holding
-- it.
--
-- A channel's blocks never overlap, so the block is the first one ending
-- at or after $3: found by one probe of the (connection_id, channel,
-- last_id) index (ORDER BY ... LIMIT 1), then checked to start at or
-- before $3. Asking for first_id <= $3 AND last_id >= $3 instead leaves
-- the planner to read every block of the channel (all 3,010 of the
-- busiest channel in production), since it can't know only one matches.
SELECT id, message::text, connection_id, highlight
FROM logs
WHERE id = $3::int AND connection_id = $1 AND command IN ('PRIVMSG', 'NOTICE')
UNION ALL
SELECT (e->>'id')::int, (e->'message')::text, a.connection_id, (e->>'highlight')::bool
FROM (
  SELECT connection_id, first_id, messages
  FROM log_archives
  WHERE connection_id = $1 AND channel = $2 AND last_id >= $3::int
  ORDER BY last_id
  LIMIT 1
) a, json_array_elements(a.messages) e
WHERE a.first_id <= $3::int
  AND (e->>'id')::int = $3::int
  AND e->'message'->>'Command' IN ('PRIVMSG', 'NOTICE')
LIMIT 1
