create table "user" (
  id       varchar(24) not null,
  email    varchar(256) not null,
  password varchar(256) not null,
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
  connection  char(24) not null,
  channel     char(32) not null,
  time        timestamp not null,
  message     jsonb not null,
  primary key (id)
);

create index on log (connection, channel, time);

create table "pref" (
  "user"  varchar(24) not null,
  name    varchar(128) not null,
  value   bytea not null,
  primary key ("user", name)
)
