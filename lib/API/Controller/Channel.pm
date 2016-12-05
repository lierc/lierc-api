package API::Controller::Channel;

use parent 'API::Controller';

use List::Util qw(min);
use Encode;

API->register( "channel.logs",     [__PACKAGE__, "logs"]);
API->register( "channel.logs_id",  [__PACKAGE__, "logs_id"]);
API->register( "channel.set_seen", [__PACKAGE__, "set_seen"]);

sub logs {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = decode utf8 => $captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);
  my $logs = $self->find_logs($chan, $id, $limit);

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
  return $self->json($data);
}

sub logs_id {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = decode utf8 => $captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);
  my $event = $captures->{event};

  my $rows = $self->dbh->selectall_arrayref(q{
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
  return $self->json($data);
}


sub set_seen {
  my ($self, $req, $captures, $session) = @_;

  my $user = $session->{user};
  my $channel = $captures->{channel};
  my $connection = $captures->{id};
  my $position = $req->content;

  my ($value) = $self->dbh->do(q{
    INSERT into seen ("user", connection, channel, message_id)
    VALUES(?,?,?,?)
    ON CONFLICT ("user", connection, channel)
    DO UPDATE SET message_id=?
    WHERE seen.user=? AND seen.connection=? AND seen.channel=?
    }, {},
    $user, $connection, $channel, $position,
    $position, $user, $connection, $channel
  );

  $self->ok;
}

1;
