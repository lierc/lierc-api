package API::Controller::Channel;

use parent 'API::Controller';

use List::Util qw(min);
use Encode;

API->register("channel.logs",     __PACKAGE__);
API->register("channel.logs_id",  __PACKAGE__);
API->register("channel.set_seen", __PACKAGE__);

sub logs {
  my ($app, $req) = @_;
  my $id   = $req->captures->{id};
  my $chan = decode utf8 => $req->captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);
  my $logs = $app->find_logs($chan, $id, $limit);

  my $json = JSON::XS->new;
  my $data = [
    map {
      {
        MessageId    => $_->[0],
        Message      => $json->decode($_->[1]),
        ConnectionId => $_->[2],
        Self         => $_->[3] ? \1 : \0,
      }
    } @$logs
  ];
  return $app->json($data);
}

sub logs_id {
  my ($app, $req) = @_;
  my $id   = $req->captures->{id};
  my $chan = decode utf8 => $req->captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);
  my $event = $req->captures->{event};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT id, message, connection, self FROM log
      WHERE channel=? AND connection=? AND id < ?
      ORDER BY id DESC LIMIT ?
    });
  $sth->execute($chan, $id, $event, $limit);

  my $json = JSON::XS->new;
  my @data;

  while (my $row = $sth->fetchrow_arrayref) {
    push @data, {
      MessageId    => $row->[0],
      Message      => $json->decode($row->[1]),
      ConnectionId => $row->[2],
      Self         => $row->[3] ? \1 : \0,
    };
  }

  $sth->finish;

  return $app->json(\@data);
}


sub set_seen {
  my ($app, $req) = @_;

  my $user = $req->session->{user};
  my $channel = $req->captures->{channel};
  my $connection = $req->captures->{id};
  my $position = $req->content;

  my $sth = $app->dbh->prepare_cached(q{
    INSERT into seen ("user", connection, channel, message_id)
    VALUES(?,?,?,?)
    ON CONFLICT ("user", connection, channel)
    DO UPDATE SET message_id=?
    WHERE seen.user=? AND seen.connection=? AND seen.channel=?
    }
  );
  $sth->execute(
    $user, $connection, $channel, $position,
    $position, $user, $connection, $channel
  );
  $sth->finish;

  $app->ok;
}

1;