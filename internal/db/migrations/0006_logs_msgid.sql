-- Replies look up the message they answer by its IRC msgid (lierc-web's
-- +reply). Not unique: NICK and QUIT are logged once per channel with one
-- msgid. Production gets it CONCURRENTLY by hand before this deploys, so
-- IF NOT EXISTS makes this a no-op there; elsewhere (tests, the dev
-- stack) it builds here.
CREATE INDEX IF NOT EXISTS logs_connection_id_msgid_idx
  ON logs (connection_id, (message->'Tags'->>'msgid'))
  WHERE message->'Tags'->>'msgid' IS NOT NULL;
