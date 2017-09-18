package API::Controller::Message;

use parent 'API::Controller';

API->register("message.log",      __PACKAGE__);
API->register("message.missed",   __PACKAGE__);
API->register("message.seen",     __PACKAGE__);

sub missed {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my (@where, @bind, @connections);

  push @where, "FALSE";

  for my $key (keys %{ $req->parameters }) {
    my ($connection, $channel) = split "-", $key, 2;
    push @where, "(log.connection=? AND log.channel=? AND log.id > ?)";
    push @bind, $connection, $channel, $req->parameters->{$key};
    push @connections, $connection;
  }

  return $app->json({}) unless @connections;

  my $err = $app->dbh->selectcol_arrayref(q{
    SELECT id
    FROM connection
    WHERE "user" <> ?
      AND id IN (
  } . join(", ", map "?", 1 .. @connections) . q{
  )}, {}, $user, @connections);

  die "Invalid connection (" . join(", ", @$err) . ")"
    if @$err;

  my $sth = $app->dbh->prepare(q{
    SELECT channel, connection,
      SUM( CASE WHEN command = 'PRIVMSG' THEN 1 ELSE 0 END ) AS messages,
      SUM( CASE WHEN command <> 'PRIVMSG' THEN 1 ELSE 0 END ) AS events
    FROM log
    WHERE
    (
  } . join(" OR ", @where) . q{
    )
    GROUP BY channel, connection
  });

  my %channels;

  $sth->execute(@bind);

  while (my $row = $sth->fetchrow_hashref) {
    my $conn = $row->{connection};
    my $chan = $row->{channel};
    $channels{ $conn }{ $chan }{messages} += $row->{messages};
    $channels{ $conn }{ $chan }{events} += $row->{events};
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

sub seen {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT s.connection, s.channel, s.message_id
    FROM seen AS s
    JOIN connection AS c
      ON c.id=s.connection
    WHERE s."user"=?
  });
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  return $app->json($rows);
}

1;
