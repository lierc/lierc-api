package API::Controller::Pref;

use parent 'API::Controller';

API->register( "pref.show",   [__PACKAGE__, "show"]);
API->register( "pref.list",   [__PACKAGE__, "list"]);
API->register( "pref.upsert", [__PACKAGE__, "upsert"]);

sub list {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT name, value FROM pref
    WHERE "user"=?
  }, {Slice => {}}, $user);

  return $self->not_found unless $rows;
  return $self->json($rows);
}

sub show {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  my $row = $self->dbh->selectrow_hashref(q{
    SELECT name, value FROM pref
    WHERE "user"=? AND name=?
  }, {}, $user, $pref);

  return $self->not_found unless $row;
  return $self->json($row);
}

sub upsert {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  $self->dbh->do(q{
    INSERT INTO pref ("user",name,value) VALUES(?,?,?)
    ON CONFLICT ("user", name)
    DO UPDATE SET value=? WHERE pref.user=? AND pref.name=?
    }, {},
    $user, $pref, $req->content,
    $req->content, $user, $pref
  );

  $self->ok;
}

1;
