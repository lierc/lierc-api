package API::Controller::Channel;

use parent 'API::Controller';

use Util;
use JSON::XS;
use List::Util qw(min);
use Encode;

API->register("channel.logs",     __PACKAGE__);
API->register("channel.logs_id",  __PACKAGE__);
API->register("channel.set_seen", __PACKAGE__);
API->register("channel.last",     __PACKAGE__);
API->register("channel.date",     __PACKAGE__);
API->register("channel.list",     __PACKAGE__);

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $id = $req->captures->{id};

  my $channels = $app->dbh->selectcol_arrayref(q{
    SELECT channel
    FROM log
    WHERE connection=?
    GROUP BY channel
    ORDER BY MAX(id) DESC
  }, {}, $id);

  $app->json($channels);
}

sub date {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $from = $req->captures->{from};
  my $to   = $req->captures->{to};
  my $chan = lc decode utf8 => $req->captures->{channel};
  my $id   = $req->captures->{id};

  return sub  {
    my $respond = shift;
    my $writer = $respond->($app->event_stream);
    my @bind;

    my $sql;
    if ($req->parameters->{text}) {
      $sql = qq!
        SELECT id,
          jsonb_set(
            message, '{Params,1}', ts_headline(
              'english', message->'Params'->1, to_tsquery(\$5),
              'StartSel = \x02, StopSel = \x02')),
          message, connection, highlight
        FROM log
        WHERE connection=\$1
          AND channel=\$2
          AND time >= date(\$3)
          AND time < (date(\$4) + '1 day'::interval)
          AND to_tsvector('english', message->'Params'->1) @@ to_tsquery(\$5)
      !;
      push @bind, ($id, $chan, $from, $to);
      push @bind, decode utf8 => $req->parameters->{text};
    }
    else {
      $sql = q!
        SELECT id, message, connection, highlight
        FROM log
        WHERE connection=$1
          AND channel=$2
          AND time >= date($3)
          AND time < (date($4) + '1 day'::interval)
      !;
      push @bind, ($id, $chan, $from, $to);
    }

    warn $sql;
    my $sth = $app->dbh->prepare_cached($sql, {
      pg_placeholder_dollaronly => 1
    });

    $sth->execute(@bind);

    my $json = JSON::XS->new;

    while (my $row = $sth->fetchrow_arrayref) {
      my $msg = {
        MessageId    => $row->[0],
        Message      => $json->decode($row->[1]),
        ConnectionId => $row->[2],
        Highlight    => $row->[3] ? \1 : \0,
      };
      $writer->write(Util->event(log => $json->encode($msg)));
    }

    $writer->close;
  };
}

sub last {
  my ($app, $req) = @_;
  my $id   = $req->captures->{id};
  my $chan = lc decode utf8 => $req->captures->{channel};
  my $limit = min($req->parameters->{limit} || 5, 20);
  my $query = decode utf8 => $req->parameters->{query};

  my $sth = $app->dbh->prepare_cached(qq!
    SELECT id,
      jsonb_set(
        message, '{Params,1}', ts_headline(
          'english', message->'Params'->1, to_tsquery(\$3),
          'StartSel = \x02, StopSel = \x02')),
      connection, highlight
    FROM log
    WHERE channel=\$1
      AND connection=\$2
      AND command='PRIVMSG'
      AND time > (NOW() - '1 week'::interval)
      AND to_tsvector(message->'Params'->>1) @@ to_tsquery(\$3)
    ORDER BY id DESC LIMIT \$4
  !, { pg_placeholder_dollaronly => 1 });
  $sth->execute($chan, $id, $query, $limit);

  my $json = JSON::XS->new;
  my @data;

  while (my $row = $sth->fetchrow_arrayref) {
    push @data, {
      MessageId    => $row->[0],
      Message      => $json->decode($row->[1]),
      ConnectionId => $row->[2],
      Highlight    => $row->[3] ? \1 : \0,
    };
  }

  $sth->finish;
  return $app->json(\@data);
}

sub logs {
  my ($app, $req) = @_;
  my $id   = $req->captures->{id};
  my $chan = lc decode utf8 => $req->captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);

  my $sth = $app->dbh->prepare_cached(q{
    SELECT id, message, connection, highlight FROM log
      WHERE channel=? AND connection=?
      ORDER BY id DESC LIMIT ?
  });
  $sth->execute($chan, $id, $limit);

  my $json = JSON::XS->new;
  my @data;

  while (my $row = $sth->fetchrow_arrayref) {
    push @data, {
      MessageId    => $row->[0],
      Message      => $json->decode($row->[1]),
      ConnectionId => $row->[2],
      Highlight    => $row->[3] ? \1 : \0,
    };
  }

  $sth->finish;
  return $app->json(\@data);
}

sub logs_id {
  my ($app, $req) = @_;
  my $id   = $req->captures->{id};
  my $chan = lc decode utf8 => $req->captures->{channel};
  my $limit = min($req->parameters->{limit} || 50, 150);
  my $event = $req->captures->{event};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT id, message, connection, highlight FROM log
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
      Highlight    => $row->[3] ? \1 : \0,
    };
  }

  $sth->finish;
  return $app->json(\@data);
}


sub set_seen {
  my ($app, $req) = @_;

  my $user = $req->session->{user};
  my $channel = lc decode utf8 => $req->captures->{channel};
  my $connection = $req->captures->{id};
  my $position = $req->content;

  my $sth = $app->dbh->prepare_cached(q{
    INSERT into seen ("user", connection, channel, message_id)
    VALUES(?,?,?,?)
    ON CONFLICT ("user", connection, channel)
    DO UPDATE SET message_id=?, updated=NOW()
    WHERE seen.user=? AND seen.connection=? AND seen.channel=?
    }
  );
  $sth->execute(
    $user, $connection, $channel, $position,
    $position, $user, $connection, $channel
  );
  $sth->finish;

  $app->nocontent;
}

1;
