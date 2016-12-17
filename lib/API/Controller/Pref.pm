package API::Controller::Pref;

use parent 'API::Controller';

API->register("pref.show",   __PACKAGE__);
API->register("pref.list",   __PACKAGE__);
API->register("pref.upsert", __PACKAGE__);

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT name, value FROM pref
    WHERE "user"=?
  });
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  return $app->not_found unless $rows;
  return $app->json($rows);
}

sub show {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT name, value FROM pref
    WHERE "user"=? AND name=?
  });
  $sth->execute($user, $pref);
  my $row = $sth->fetchrow_hashref;
  $sth->finish;

  return $app->not_found unless $row;
  return $app->json($row);
}

sub upsert {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  my $sth = $app->dbh->pepare_cached(q{
    INSERT INTO pref ("user",name,value) VALUES(?,?,?)
    ON CONFLICT ("user", name)
    DO UPDATE SET value=? WHERE pref.user=? AND pref.name=?
    }
  );
  $sth->execute(
    $user, $pref, $req->content,
    $req->content, $user, $pref
  );
  $sth->finish;

  $app->ok;
}

1;
