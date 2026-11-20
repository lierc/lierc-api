-- logger.insert: message $4 (command $3, at unix time $5, self $6, a
-- highlight $7) once under each of channels $2, in order, for connection
-- $1's owner, returning the last id: NULL when nothing was stored, for
-- want of the connection. One statement for all the channels, so the last
-- id is the last channel's.
--
-- A highlight also goes in highlights, which /highlight reads and
-- archiving leaves alone: same id, same statement.
WITH logged AS (
  INSERT INTO logs (user_id, connection_id, channel, command, message, time, self, highlight)
  SELECT connections.user_id, connections.id, c.channel, $3, $4, to_timestamp($5), $6, $7
  FROM connections, unnest($2::text[]) WITH ORDINALITY AS c(channel, n)
  WHERE connections.id = $1
  ORDER BY c.n
  RETURNING id, user_id, connection_id, channel, time, message, self, highlight
), highlighted AS (
  INSERT INTO highlights (id, user_id, connection_id, channel, time, message, self)
  SELECT id, user_id, connection_id, channel, time, message, self FROM logged WHERE highlight
)
SELECT max(id) FROM logged
