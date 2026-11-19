-- channel.date: the messages of connection $1, channel $2 from date $3
-- through date $4, oldest first, from logs and log_archives; when $5 is not
-- NULL, only those whose text matches $5 (websearch_to_tsquery: plain words,
-- "phrases", or, -word; never a syntax error), and when $6 is not
-- NULL, only those sent by $6; at most $7, the oldest. The dates are days
-- in time zone $8 (an IANA name such as America/New_York): from $3's
-- midnight there up to the midnight after $4, so a day that changes
-- daylight saving time is 23 or 25 hours long.
--
-- Only the archive blocks whose time span overlaps the range are unpacked.
-- Their elements are filtered by their own time (whole seconds, which never
-- moves a message across a day), not by the block's span.
SELECT id, message::text AS message, connection_id, highlight
FROM logs
WHERE connection_id = $1
  AND channel = $2
  AND time >= (date($3)::timestamp AT TIME ZONE $8::text)
  AND time < ((date($4) + 1)::timestamp AT TIME ZONE $8::text)
  AND ($5::text IS NULL OR to_tsvector('english', message->'Params'->1) @@ websearch_to_tsquery($5::text))
  AND ($6::text IS NULL OR message->'Prefix'->>'Name' = $6::text)
UNION ALL
SELECT id, message, connection_id, highlight FROM (
  SELECT (e->>'id')::int AS id, e->'message' AS m, (e->'message')::text AS message,
    a.connection_id, (e->>'highlight')::bool AS highlight,
    to_timestamp((e->>'time')::bigint) AS time
  FROM log_archives a
  CROSS JOIN LATERAL json_array_elements(a.messages) e
  WHERE a.connection_id = $1
    AND a.channel = $2
    AND a.min_time < ((date($4) + 1)::timestamp AT TIME ZONE $8::text)
    AND a.max_time >= (date($3)::timestamp AT TIME ZONE $8::text)
) archived
WHERE time >= (date($3)::timestamp AT TIME ZONE $8::text)
  AND time < ((date($4) + 1)::timestamp AT TIME ZONE $8::text)
  AND ($5::text IS NULL OR to_tsvector('english', m->'Params'->1) @@ websearch_to_tsquery($5::text))
  AND ($6::text IS NULL OR m->'Prefix'->>'Name' = $6::text)
ORDER BY id ASC
LIMIT $7::int
