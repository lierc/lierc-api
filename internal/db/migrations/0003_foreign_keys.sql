-- Every row references the user or connection it belongs to, and goes
-- with it: deleting a connection deletes its history, seen markers and
-- private chats; deleting a user deletes its connections (and so theirs),
-- prefs, web push subscriptions and logins. Rows whose owner is already
-- gone (left by deletes before this) are deleted first, or the keys
-- couldn't be added.

-- rows whose connection is gone
DELETE FROM logs l WHERE NOT EXISTS (SELECT 1 FROM connections c WHERE c.id = l.connection_id);
DELETE FROM log_archives a WHERE NOT EXISTS (SELECT 1 FROM connections c WHERE c.id = a.connection_id);
DELETE FROM seen_markers s WHERE NOT EXISTS (SELECT 1 FROM connections c WHERE c.id = s.connection_id);
DELETE FROM private_chats p WHERE NOT EXISTS (SELECT 1 FROM connections c WHERE c.id = p.connection_id);

-- rows whose user is gone; connections last, with their rows (the
-- deploy stops such a connection in liercd first)
DELETE FROM logs l WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = l.user_id)
  OR l.connection_id IN (SELECT id FROM connections c WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id));
DELETE FROM log_archives a WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = a.user_id)
  OR a.connection_id IN (SELECT id FROM connections c WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id));
DELETE FROM seen_markers s WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = s.user_id)
  OR s.connection_id IN (SELECT id FROM connections c WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id));
DELETE FROM private_chats p
  WHERE p.connection_id IN (SELECT id FROM connections c WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id));
DELETE FROM prefs p WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = p.user_id);
DELETE FROM web_push_subscriptions w WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = w.user_id);
DELETE FROM connections c WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id);

-- a cascade looks rows up by the referencing column; these two had no
-- index leading with it
CREATE INDEX log_archives_user_id_idx ON log_archives (user_id);
CREATE INDEX seen_markers_connection_id_idx ON seen_markers (connection_id);

ALTER TABLE connections
  ADD CONSTRAINT connections_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE logs
  ADD CONSTRAINT logs_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES connections (id) ON DELETE CASCADE,
  ADD CONSTRAINT logs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE log_archives
  ADD CONSTRAINT log_archives_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES connections (id) ON DELETE CASCADE,
  ADD CONSTRAINT log_archives_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE seen_markers
  ADD CONSTRAINT seen_markers_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES connections (id) ON DELETE CASCADE,
  ADD CONSTRAINT seen_markers_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE private_chats
  ADD CONSTRAINT private_chats_connection_id_fkey FOREIGN KEY (connection_id) REFERENCES connections (id) ON DELETE CASCADE;
ALTER TABLE prefs
  ADD CONSTRAINT prefs_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE web_push_subscriptions
  ADD CONSTRAINT web_push_subscriptions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
