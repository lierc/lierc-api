-- logger.insert: message $4 (command $3, at unix time $5, self $6) under
-- channel $2 for connection $1's owner, returning its id; no row when
-- there's no such connection.
--
-- A highlight ($7) also goes in highlights, which /highlight reads and
-- archiving leaves alone: same id, same statement.
WITH logged AS (
  INSERT INTO logs (user_id, connection_id, channel, command, message, time, self, highlight)
  SELECT connections.user_id, connections.id, $2, $3, $4, to_timestamp($5), $6, $7
  FROM connections WHERE connections.id = $1
  RETURNING id, user_id, connection_id, channel, time, message, self, highlight
), highlighted AS (
  INSERT INTO highlights (id, user_id, connection_id, channel, time, message, self)
  SELECT id, user_id, connection_id, channel, time, message, self FROM logged WHERE highlight
)
SELECT id FROM logged
