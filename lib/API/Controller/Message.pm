package API::Controller::Message;

use parent 'API::Controller';

API->register( "message.missed",   [__PACKAGE__, "missed"]);
API->register( "message.privates", [__PACKAGE__, "privates"]);
API->register( "message.seen",     [__PACKAGE__, "seen"]);

sub missed {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my (@where, @bind);

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

sub privates {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $rows = $app->dbh->selectall_arrayref(q{
    SELECT DISTINCT(log.channel) as nick, log.connection
    FROM log
    JOIN connection
      ON log.connection=connection.id
    JOIN "user"
      ON connection.user="user".id
    WHERE log.privmsg = TRUE
    AND connection.user=?
    AND (
      log.time > NOW() - INTERVAL '2 days'
      OR log.id > "user".last_id
    )
  }, {Slice => {}}, $user);

  $self->json([ grep { $_->{nick} =~ /^[^#&+!]/ } @$rows ]);
}

sub seen {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $rows = $app->dbh->selectall_arrayref(q{
    SELECT connection,channel,message_id FROM seen WHERE "user"=?
  }, {Slice => {}}, $user);

  return $app->json($rows);
}

1;
