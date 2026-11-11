-- channel.last: the newest $4 PRIVMSGs from the last week in channel $1 on
-- connection $2 whose text matches $3 (websearch_to_tsquery), newest first,
-- with the match wrapped in \x02 by ts_headline.
SELECT id,
  jsonb_set(message, '{Params,1}', ts_headline('english', message->'Params'->1,
    websearch_to_tsquery($3::text), E'StartSel = \x02, StopSel = \x02'))::text AS message,
  connection, highlight
FROM log
WHERE channel = $1 AND connection = $2 AND command = 'PRIVMSG'
  AND time > (NOW() - '1 week'::interval)
  AND to_tsvector(message->'Params'->>1) @@ websearch_to_tsquery($3::text)
ORDER BY id DESC LIMIT $4::int
