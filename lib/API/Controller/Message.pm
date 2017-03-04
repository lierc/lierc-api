package API::Controller::Message;

use parent 'API::Controller';

API->register("message.log",      __PACKAGE__);
API->register("message.missed",   __PACKAGE__);
API->register("message.privates", __PACKAGE__);
API->register("message.seen",     __PACKAGE__);

sub missed {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my (@where, @bind);

  push @where, "FALSE";

  for my $key (keys %{ $req->parameters }) {
    my ($connection, $channel) = split "-", $key, 2;
    push @where, sprintf "(log.connection=? AND log.channel=? AND log.id > ?)";
    push @bind, $connection, $channel, $req->parameters->{$key};
  }

  my $sth = $app->dbh->prepare(q{
    SELECT COUNT(*), channel, privmsg, connection
    FROM log
    JOIN connection
      ON connection.id=log.connection
    WHERE
      connection."user"=?
    AND (
  } . join(" OR ", @where) . q{
    )
    GROUP BY channel, connection, privmsg
  });

  my %channels;

  $sth->execute($user, @bind);

  while (my $row = $sth->fetchrow_hashref) {
    my $key = $row->{privmsg} ? "messages" : "events";
    my $conn = $row->{connection};
    my $chan = $row->{channel};
    $channels{ $conn }{ $chan }{ $key } += $row->{count};
  }

  $app->json(\%channels);
}

sub log {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $start = $req->captures->{event};

  my $sth = $app->dbh->prepare(q{
    SELECT l.id, l.message, l.connection, l.self, l.highlight
    FROM log AS l
    JOIN connection AS c
      ON c.id=l.connection
    WHERE
      c."user"=?
      AND l.id > ?
    ORDER BY l.id DESC
  });

  $sth->execute($user, $start);

  if ($sth->rows > 250) {
    return $app->error("Too many messages!")
  }

  my $json = JSON::XS->new;
  my @data;

  while (my $row = $sth->fetchrow_arrayref) {
    unshift @data, {
      MessageId    => $row->[0],
      Message      => $json->decode($row->[1]),
      ConnectionId => $row->[2],
      Self         => $row->[3] ? \1 : \0,
      Highlight    => $row->[4] ? \1 : \0,
    };
  }

  $sth->finish;
  return $app->json(\@data);
}

sub privates {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT DISTINCT(log.channel) as nick, log.connection
    FROM log
    JOIN connection
      ON log.connection=connection.id
    JOIN "user"
      ON connection.user="user".id
    WHERE log.privmsg = TRUE
    AND connection.user=?
    AND log.time > LEAST(
      "user".last_login, NOW() - INTERVAL '2 DAY'
    )
  });

  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  $app->json([ grep { $_->{nick} =~ /^[^#&+!]/ } @$rows ]);
}

sub seen {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT connection,channel,message_id FROM seen WHERE "user"=?
  });
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  return $app->json($rows);
}

1;
