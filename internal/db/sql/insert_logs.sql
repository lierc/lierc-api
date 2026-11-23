-- logger.insert (InsertLog, in its transaction): message $4 (command $3, at
-- unix time $5, self $6, a highlight $7) once under each of channels $2,
-- in order, for connection $1's owner; the ids and channels stored, in
-- order. No rows when the connection doesn't exist.
WITH logged AS (
  INSERT INTO logs (user_id, connection_id, channel, command, message, time, self, highlight)
  SELECT connections.user_id, connections.id, c.channel, $3, $4, to_timestamp($5), $6, $7
  FROM connections, unnest($2::text[]) WITH ORDINALITY AS c(channel, n)
  WHERE connections.id = $1
  ORDER BY c.n
  RETURNING id, channel
)
SELECT id, channel FROM logged ORDER BY id
