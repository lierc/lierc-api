-- logger.insert_many: message $4 (command $3, at unix time $5, self $6)
-- once under each of channels $2 for connection $1's owner, never a
-- highlight, returning the last id: NULL when nothing was stored.
--
-- One statement for all the channels, in order, so the last id is the
-- last channel's.
WITH inserted AS (
  INSERT INTO logs (user_id, connection_id, channel, command, message, time, self, highlight)
  SELECT connections.user_id, connections.id, c.channel, $3, $4, to_timestamp($5), $6, false
  FROM connections, unnest($2::text[]) WITH ORDINALITY AS c(channel, n)
  WHERE connections.id = $1
  ORDER BY c.n
  RETURNING id
)
SELECT max(id) FROM inserted
