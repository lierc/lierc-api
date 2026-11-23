-- MessageByMsgid, first: where connection $1's copy of msgid $2 was
-- logged, its channel and logs id, from message_ids (never archived),
-- through message_ids_msgid_idx. Each connection logs its own copy of a
-- message, so the msgid alone isn't enough; the earliest if it was logged
-- twice.
SELECT c.channel, m.log_id
FROM message_ids m JOIN channels c ON c.id = m.channel_id
WHERE m.msgid = $2 AND c.connection_id = $1
ORDER BY m.log_id
LIMIT 1
