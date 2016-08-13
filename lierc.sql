create table "user" (
  id varchar(24) not null,
  email varchar(256) not null,
  password varchar(256) not null,
  primary key (id),
  unique (email)
);

create table connection (
  "user" varchar(24) not null,
  id   varchar(24) not null,
  config json not null
);

CREATE TABLE log (
  connection char(24) not null,
  channel    char(32) not null,
  time       timestamp without time zone default (now() at time zone 'utc'),
  message    varchar(512) not null,
  PRIMARY KEY (connection, channel, time)
);
