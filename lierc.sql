create table "user" (
  id       varchar(24) not null,
  email    varchar(256) not null,
  password varchar(256) not null,
  username varchar(256) not null,
  last_login timestamp not null default NOW(),
  unique (email),
  unique (username),
  primary  key (id)
);

create unique index on "user" (email);

create table connection (
  id      varchar(24) not null,
  "user"  varchar(24) not null,
  config  json not null,
  primary key (id)
);

create index on connection ("user");

create table log (
  id          serial,
  connection  varchar(24) not null,
  channel     varchar(32) not null,
  privmsg     bool not null default false,
  highlight   bool not null default false,
  time        timestamp not null,
  message     jsonb not null,
  self        bool not null default false,
  primary key (id)
);

create index on log (connection, channel, highlight DESC);
create index on log (connection, channel, id DESC);
create index on log (connection, channel, privmsg, id DESC);
create index on log (time);

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
  primary key ("user", connection, channel)
);

create table token (
  id varchar(24) not null,
  "user" varchar(24) not null,
  created timestamp not null,
  primary key (id)
);

create index on token ("user", id);

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

create table ignore (
  connection varchar(24) not null,
  channel varchar(32) not null,
  "from" varchar(128) not null,
  primary key (connection, channel, "from")
);
