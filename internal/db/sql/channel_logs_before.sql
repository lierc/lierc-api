-- channel.logs_id: the newest $4 messages of channel $1 on connection $2
-- with ids below $3, newest first, from log and then from log_archive.
--
-- Archived ids are all below the ids left in log for a channel, so the
-- archive is read only when log has fewer than $4 rows below $3, and then
-- only the newest blocks starting below $3 whose counts are needed to make
-- up the rest. The one block that can straddle $3 counts as empty, since
-- only part of it is below $3, so it never cuts the blocks short.
WITH l AS (
  SELECT id, message::text AS message, connection, highlight
  FROM log
  WHERE channel = $1 AND connection = $2 AND id < $3::int
  ORDER BY id DESC
  LIMIT $4::int
),
need AS (
  SELECT greatest($4::int - count(*)::int, 0) AS n FROM l
),
newest AS (
  SELECT id, last_id, count
  FROM log_archive
  WHERE connection = $2 AND channel = $1 AND first_id < $3::int
  ORDER BY last_id DESC
  LIMIT (SELECT n FROM need)
),
blocks AS (
  SELECT id FROM (
    SELECT id, coalesce(sum(CASE WHEN last_id < $3::int THEN count ELSE 0 END) OVER (
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
  WHERE (e->>'id')::int < $3::int
  ORDER BY 1 DESC
  LIMIT (SELECT n FROM need)
) archived
ORDER BY id DESC
