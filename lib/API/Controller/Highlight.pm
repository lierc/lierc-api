package API::Controller::Highlight;

use parent 'API::Controller';

API->register("highlight.create", __PACKAGE__);
API->register("highlight.list",   __PACKAGE__);
API->register("highlight.delete", __PACKAGE__);

sub delete {
  my ($app, $req) = @_;
  my $connection = $req->captures->{id};
  my $string     = lc decode utf8 => $req->captures->{string};

  my $sth = $app->prepare_cached(q{
    DELETE FROM highlight
    WHERE connection=?
      AND string=?
  });
  $sth->execute($connection, $string);
  $sth->finish;

  $app->dbh->do("NOTIFY highlights");

  $sth->nocontent;
}

sub create {
  my $connection = $req->captures->{id};
  my $string     = lc decode utf8 => $req->captures->{string};

  my $sth = $app->prepare_cached(q{
    INSERT INTO highlight
    (connection, string)
    VALUES(?,?)
  });
  $sth->execute($connection, $string);
  $sth->finish;

  $app->dbh->do("NOTIFY highlights");

  $app->ok;
}

sub list {
  my $connection = $req->captures->{id};

  my $sth = $app->prepare_cached(q{
    SELECT string
    FROM highlight
    WHERE connection=?
  });
  $sth->execute($connection);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  $app->json($rows);
}

1;
