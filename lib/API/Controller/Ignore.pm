package API::Controller::Ignore;

use parent 'API::Controller';
use Encode;

API->register("ignore.create", __PACKAGE__);
API->register("ignore.delete", __PACKAGE__);
API->register("ignore.list",   __PACKAGE__);

sub delete {
  my ($app, $req) = @_;
  my $connection = $req->captures->{id};
  my $channel    = $req->captures->{channel};
  my $from       = $req->captures->{from};

  my $sth = $app->dbh->prepare_cached(q{
    DELETE FROM ignore
    WHERE connection=?
      AND channel=?
      AND "from"=?
  });

  $sth->execute($connection, $channel, $from);
  $sth->finish;
  $app->nocontent;
}

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT i.* FROM ignore AS i
    JOIN connection AS c
      ON c.id=i.connection
    WHERE c."user"=?
  });
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  return $app->json($rows);
}

sub create {
  my ($app, $req) = @_;
  my $connection = $req->captures->{id};
  my $channel     = lc decode utf8 => $req->captures->{channel};
  my $from        = decode utf8 => $req->parameters->{from};

  my $sth = $app->dbh->prepare_cached(q{
    INSERT INTO ignore
    (connection, channel, "from")
    VALUES(?,?,?)
  });
  $sth->execute($connection, $channel, $from);
  $sth->finish;

  return $app->ok;
}

1;
