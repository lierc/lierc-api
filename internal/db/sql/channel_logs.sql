-- channel.logs: the newest $3 messages of channel $1 on connection $2,
-- newest first, from log and then from log_archive.
--
-- Archived ids are all below the ids left in log for a channel, so the
-- archive is read only when log has fewer than $3 rows, and then only the
-- newest blocks whose counts are needed to make up the rest.
WITH l AS (
  SELECT id, message::text AS message, connection, highlight
  FROM log
  WHERE channel = $1 AND connection = $2
  ORDER BY id DESC
  LIMIT $3::int
),
need AS (
  SELECT greatest($3::int - count(*)::int, 0) AS n FROM l
),
newest AS (
  SELECT id, last_id, count
  FROM log_archive
  WHERE connection = $2 AND channel = $1
  ORDER BY last_id DESC
  LIMIT (SELECT n FROM need)
),
blocks AS (
  SELECT id FROM (
    SELECT id, coalesce(sum(count) OVER (
      ORDER BY last_id DESC ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING
    ), 0) AS newer
    FROM newest
  ) w
  WHERE newer < (SELECT n FROM need)
)
SELECT id, message, connection, highlight FROM l
UNION ALL
SELECT * FROM (
  SELECT (e->>'id')::int AS id, (e->'message')::text AS message, a.connection,
    (e->>'highlight')::bool AS highlight
  FROM log_archive a
  JOIN blocks b ON b.id = a.id
  AND a.connection = $2 AND a.channel = $1
  CROSS JOIN LATERAL json_array_elements(a.messages) e
  ORDER BY 1 DESC
  LIMIT (SELECT n FROM need)
) archived
ORDER BY id DESC
