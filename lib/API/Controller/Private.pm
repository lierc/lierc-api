package API::Controller::Message;

use Encode;

use parent 'API::Controller';

API->register("private.list", __PACKAGE__);
API->register("private.delete", __PACKAGE__);
API->register("private.create", __PACKAGE__);

sub create {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $conn = $req->captures->{id};
  my $nick = decode utf8 => $req->captures->{nick};

  my $sth = $app->dbh->prepare_cached(q{
    INSERT INTO private (connection, nick, time)
    VALUES($1,$2,NOW())
    ON CONFLICT (connection, nick)
    DO UPDATE SET time=NOW()
    WHERE private.connection=$1 AND private.nick=$2
  }, { pg_placeholder_dollaronly => 1 });
  $sth->execute($conn, $nick);
  $app->nocontent;
}

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT p.nick, p.connection, p.time
    FROM private AS p
    JOIN connection AS c
      ON c.id=p.connection
    WHERE c.user=?
    ORDER BY p.time ASC
  });

  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  $app->json($rows);
}

sub delete {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $conn = $req->captures->{id};
  my $nick = decode utf8 => $req->captures->{nick};

  my $sth = $app->dbh->prepare_cached(q{
    DELETE FROM private AS p
    WHERE p.connection=?
      AND p.nick=?
  });
  $sth->execute($conn, $nick);

  $app->ok;
}

1;
