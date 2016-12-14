package API::Controller::Pref;

use parent 'API::Controller';

API->register( "pref.show",   [__PACKAGE__, "show"]);
API->register( "pref.list",   [__PACKAGE__, "list"]);
API->register( "pref.upsert", [__PACKAGE__, "upsert"]);

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  my $rows = $app->dbh->selectall_arrayref(q{
    SELECT name, value FROM pref
    WHERE "user"=?
  }, {Slice => {}}, $user);

  return $app->not_found unless $rows;
  return $app->json($rows);
}

sub show {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  my $row = $app->dbh->selectrow_hashref(q{
    SELECT name, value FROM pref
    WHERE "user"=? AND name=?
  }, {}, $user, $pref);

  return $app->not_found unless $row;
  return $app->json($row);
}

sub upsert {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $pref = $req->captures->{pref};

  $app->dbh->do(q{
    INSERT INTO pref ("user",name,value) VALUES(?,?,?)
    ON CONFLICT ("user", name)
    DO UPDATE SET value=? WHERE pref.user=? AND pref.name=?
    }, {},
    $user, $pref, $req->content,
    $req->content, $user, $pref
  );

  $app->ok;
}

1;
