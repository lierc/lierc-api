create table "user" (
  id       varchar(24) not null,
  email    varchar(256) not null,
  password varchar(256) not null,
  username varchar(256) not null,
  last_id  int not null default 0,
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
  value   bytea not null,
  primary key ("user", name)
);

create table seen (
  "user" varchar(24) not null,
  connection varchar(24) not null,
  channel    varchar(32) not null,
  message_id int not null,
  primary key ("user", connection, channel)
);
