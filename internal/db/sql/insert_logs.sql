-- logger.insert: message $4 (command $3, at unix time $5, self $6, a
-- highlight $7) once under each of channels $2, in order, for connection
-- $1's owner, returning the last id: NULL when nothing was stored, for
-- want of the connection. One statement for all the channels, so the last
-- id is the last channel's.
--
-- A highlight also goes in highlights, which /highlight reads and
-- archiving leaves alone: same id, same statement.
--
-- A PRIVMSG or NOTICE with a msgid is also mapped in message_ids (msgid
-- -> id, through its channels row, made here when it's the channel's
-- first), which archiving also leaves alone: same statement, so a failed
-- insert leaves nothing behind for NSQ's retry to duplicate.
WITH logged AS (
  INSERT INTO logs (user_id, connection_id, channel, command, message, time, self, highlight)
  SELECT connections.user_id, connections.id, c.channel, $3, $4, to_timestamp($5), $6, $7
  FROM connections, unnest($2::text[]) WITH ORDINALITY AS c(channel, n)
  WHERE connections.id = $1
  ORDER BY c.n
  RETURNING id, user_id, connection_id, channel, time, message, self, highlight
), wanted AS (
  -- the logged rows to map
  SELECT id, connection_id, channel, $4::jsonb->'Tags'->>'msgid' AS msgid
  FROM logged
  WHERE $3 IN ('PRIVMSG', 'NOTICE') AND $4::jsonb->'Tags'->>'msgid' IS NOT NULL
), new_channels AS (
  INSERT INTO channels (connection_id, channel)
  SELECT DISTINCT connection_id, channel FROM wanted
  ON CONFLICT DO NOTHING
  RETURNING id, connection_id, channel
), chans AS (
  -- a data-modifying CTE's rows aren't visible to the statement's reads of
  -- the table, so the new rows come from RETURNING and the rest from here
  SELECT id, connection_id, channel FROM new_channels
  UNION ALL
  SELECT c.id, c.connection_id, c.channel
  FROM channels c JOIN (SELECT DISTINCT connection_id, channel FROM wanted) w USING (connection_id, channel)
), mapped AS (
  INSERT INTO message_ids (channel_id, msgid, log_id)
  SELECT chans.id, wanted.msgid, wanted.id
  FROM wanted JOIN chans USING (connection_id, channel)
), highlighted AS (
  INSERT INTO highlights (id, user_id, connection_id, channel, time, message, self)
  SELECT id, user_id, connection_id, channel, time, message, self FROM logged WHERE highlight
)
SELECT max(id) FROM logged
