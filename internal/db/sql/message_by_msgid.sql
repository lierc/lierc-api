-- MessageByMsgid: the earliest PRIVMSG or NOTICE on connection $1 with IRC
-- msgid $2, from logs only (archived messages aren't indexed).
--
-- The matches (a handful at most) are found first, through
-- logs_connection_id_msgid_idx, then ordered: a bare ORDER BY id LIMIT 1
-- lets the planner walk logs_pkey instead, filtering every row, which
-- reads the whole table when the msgid isn't there.
WITH matches AS MATERIALIZED (
  SELECT id, message, connection_id, highlight
  FROM logs
  WHERE connection_id = $1
    AND message->'Tags'->>'msgid' = $2
    AND command IN ('PRIVMSG', 'NOTICE')
)
SELECT id, message::text, connection_id, highlight
FROM matches
ORDER BY id
LIMIT 1
