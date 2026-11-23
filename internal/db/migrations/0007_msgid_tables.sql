-- Message ids that outlive archiving (docs: 2026-11-23 msgid tables).
-- The logger writes these in the same statement as each logs row
-- (insert_logs.sql); archiving leaves them alone, so a reply's original
-- is found by its IRC msgid even after its logs row has moved to
-- log_archives.

-- One row per channel per connection, so the others carry an integer.
CREATE TABLE channels (
  id            serial PRIMARY KEY,
  connection_id varchar(24) NOT NULL REFERENCES connections ON DELETE CASCADE,
  channel       text NOT NULL CHECK (channel !~ '[A-Z]'),
  UNIQUE (connection_id, channel)
);

-- Every logged PRIVMSG and NOTICE with a msgid: msgid -> our logs id. Not
-- unique: each connection logs its own copy of a message (one msgid, a row
-- per lierc connection that saw it), and a duplicate does no harm. No
-- foreign key on log_id: the logs row moves to log_archives.
CREATE TABLE message_ids (
  channel_id integer NOT NULL REFERENCES channels ON DELETE CASCADE,
  msgid      text NOT NULL,
  log_id     integer NOT NULL
);
CREATE INDEX message_ids_msgid_idx ON message_ids (msgid);

-- Every logged reply (+reply, or +draft/reply): what it answers, and the
-- first message of its thread (its parent's root, else the parent).
CREATE TABLE replies (
  channel_id  integer NOT NULL REFERENCES channels ON DELETE CASCADE,
  msgid       text NOT NULL,
  reply_to    text NOT NULL,
  thread_root text NOT NULL
);
CREATE INDEX replies_msgid_idx ON replies (msgid);
CREATE INDEX replies_thread_root_idx ON replies (thread_root);
