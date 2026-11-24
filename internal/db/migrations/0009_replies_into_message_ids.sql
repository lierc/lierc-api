-- A reply's links live on its own message_ids row (every reply is a
-- PRIVMSG or NOTICE with a msgid, so it has one), not in replies: one table
-- says both where a message is and what it answers, and the columns are
-- NULL, almost free, on the rows that aren't replies. A replies row with no
-- message_ids row (there shouldn't be one) goes with the table.
-- Adding nullable columns without a default doesn't rewrite the table.

-- Both locks up front, in the order the logger takes them (channels, then
-- message_ids): ALTER locks message_ids and DROP TABLE replies locks
-- channels (its foreign key's triggers), and taking them the other way
-- round could deadlock with a logger insert in flight during the deploy.
LOCK TABLE channels, message_ids IN ACCESS EXCLUSIVE MODE;

ALTER TABLE message_ids
  ADD COLUMN reply_to text,
  ADD COLUMN thread_root text,
  ADD CONSTRAINT message_ids_reply_check CHECK ((reply_to IS NULL) = (thread_root IS NULL));

UPDATE message_ids m SET reply_to = r.reply_to, thread_root = r.thread_root
FROM replies r
WHERE r.channel_id = m.channel_id AND r.msgid = m.msgid;

DROP TABLE replies;

-- the thread panel's lookup: every reply in a thread
CREATE INDEX message_ids_thread_root_idx ON message_ids (thread_root) WHERE thread_root IS NOT NULL;
