package API::Controller::Message;

use Encode;

use parent 'API::Controller';

API->register("private.list", __PACKAGE__);
API->register("private.delete", __PACKAGE__);
API->register("private.create", __PACKAGE__);

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT p.nick, p.connection, p.time
    FROM private AS p
    JOIN connection AS c
      ON c.id=p.connection
    WHERE c.user=?
    ORDER BY p.time DESC
    LIMIT 10
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
