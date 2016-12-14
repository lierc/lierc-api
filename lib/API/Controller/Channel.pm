package API::Controller::Channel;

use parent 'API::Controller';

use List::Util qw(min);
use Encode;

API->register( "channel.logs",     [__PACKAGE__, "logs"]);
API->register( "channel.logs_id",  [__PACKAGE__, "logs_id"]);
API->register( "channel.set_seen", [__PACKAGE__, "set_seen"]);

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

  my $rows = $app->dbh->selectall_arrayref(q{
    SELECT id, message, connection FROM log
      WHERE channel=? AND connection=? AND id < ?
      ORDER BY id DESC LIMIT ?
    }, {}, $chan, $id, $event, $limit
  );

  my $json = JSON::XS->new;
  my $data = [
    map {
      {
        MessageId    => $_->[0],
        Message      => $json->decode($_->[1]),
        ConnectionId => $_->[2],
      }
    } @$rows
  ];
  return $app->json($data);
}


sub set_seen {
  my ($app, $req) = @_;

  my $user = $req->session->{user};
  my $channel = $req->captures->{channel};
  my $connection = $req->captures->{id};
  my $position = $req->content;

  my ($value) = $app->dbh->do(q{
    INSERT into seen ("user", connection, channel, message_id)
    VALUES(?,?,?,?)
    ON CONFLICT ("user", connection, channel)
    DO UPDATE SET message_id=?
    WHERE seen.user=? AND seen.connection=? AND seen.channel=?
    }, {},
    $user, $connection, $channel, $position,
    $position, $user, $connection, $channel
  );

  $app->ok;
}

1;
