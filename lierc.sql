create table "user" (
  id       varchar(24) not null,
  email    varchar(256) not null,
  password varchar(256),
  password_hash varchar(60),
  username varchar(256) not null,
  last_login timestamp not null default NOW(),
  -- when the account signed up (set by signup); NULL for accounts from
  -- before the column
  registered timestamp,
  -- false: signed up but hasn't confirmed its email, so can't use the app;
  -- NULL: an account from before that, allowed in
  verified bool default false,
  -- when a verify email was last sent, to limit resends
  verify_sent timestamp,
  primary  key (id),
  -- the API's signup rule, enforced for every writer
  constraint user_username_valid check (username ~ '^[a-zA-Z0-9_.]{1,32}$')
);

-- emails and usernames are unique regardless of case
create unique index user_email_lower_key on "user" (lower(email));
create unique index user_username_lower_key on "user" (lower(username));

create table connection (
  id      varchar(24) not null,
  "user"  varchar(24) not null,
  config  json not null,
  liercd  text,
  primary key (id)
);

create index on connection ("user");

create table log (
  id          serial,
  connection  varchar(24) not null,
  "user"      varchar(24) not null,
  channel     varchar(32) not null,
  command     varchar(16),
  highlight   bool not null default false,
  time        timestamp not null,
  message     jsonb not null,
  self        bool not null default false,
  primary key (id)
);

create index on log (connection, channel, id DESC);
create index log_message_text on log USING gin ( to_tsvector( 'english', message->'Params'->1 ));
create index log_sender on log USING gin ((message->'Prefix'->'Name'));
create index log_user_highlight on log ("user", highlight, id DESC);
create index log_tag_msgid on log USING gin ((message->'Tags'->'msgid'));
create index on log (time);

create table log_archive (
  id          serial primary key,
  connection  varchar(24) not null,
  "user"      varchar(24) not null,
  channel     varchar(32) not null,
  first_id    int not null,
  last_id     int not null,
  min_time    timestamp not null,
  max_time    timestamp not null,
  count       int not null,
  messages    json not null
);

create index on log_archive (connection, channel, last_id DESC);
create index on log_archive (connection, channel, min_time);

create table pref (
  "user"  varchar(24) not null,
  name    varchar(128) not null,
  value   varchar(512) not null,
  primary key ("user", name)
);

create table seen (
  "user" varchar(24) not null,
  connection varchar(24) not null,
  channel    varchar(32) not null,
  message_id int not null,
  updated timestamp not null default now(),
  primary key ("user", connection, channel)
);

create table private (
  connection varchar(24) not null,
  nick varchar(32) not null,
  time timestamp not null,
  primary key (connection, nick)
);

create table web_push (
  endpoint varchar(255) not null,
  key varchar(255) not null,
  auth varchar(255) not null,
  "user" varchar(24) not null,
  primary key ("user", endpoint)
);
