-- channel.date: the messages of connection $1, channel $2 from date $3
-- through date $4, oldest first, from log and log_archive; when $5 is not
-- NULL, only those whose text matches $5 (websearch_to_tsquery: plain words,
-- "phrases", or, -word; never a syntax error), and when $6 is not
-- NULL, only those sent by $6.
--
-- Only the archive blocks whose time span overlaps the range are unpacked.
-- Their elements are filtered by their own time (whole seconds, which never
-- moves a message across a day), not by the block's span.
SELECT id, message::text AS message, connection, highlight
FROM log
WHERE connection = $1
  AND channel = $2
  AND time >= date($3)
  AND time < (date($4) + '1 day'::interval)
  AND ($5::text IS NULL OR to_tsvector('english', message->'Params'->1) @@ websearch_to_tsquery($5::text))
  AND ($6::text IS NULL OR message->'Prefix'->>'Name' = $6::text)
UNION ALL
SELECT id, message, connection, highlight FROM (
  SELECT (e->>'id')::int AS id, e->'message' AS m, (e->'message')::text AS message,
    a.connection, (e->>'highlight')::bool AS highlight,
    to_timestamp((e->>'time')::bigint) AT TIME ZONE 'UTC' AS time
  FROM log_archive a
  CROSS JOIN LATERAL json_array_elements(a.messages) e
  WHERE a.connection = $1
    AND a.channel = $2
    AND a.min_time < (date($4) + '1 day'::interval)
    AND a.max_time >= date($3)
) archived
WHERE time >= date($3)
  AND time < (date($4) + '1 day'::interval)
  AND ($5::text IS NULL OR to_tsvector('english', m->'Params'->1) @@ websearch_to_tsquery($5::text))
  AND ($6::text IS NULL OR m->'Prefix'->>'Name' = $6::text)
ORDER BY id ASC
