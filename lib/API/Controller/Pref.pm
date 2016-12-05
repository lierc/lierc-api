package API::Controller::Pref;

use parent 'API::Controller';

API->register( "pref.pref", [__PACKAGE__, "pref"]);
API->register( "pref.prefs", [__PACKAGE__, "prefs"]);
API->register( "pref.set_pref", [__PACKAGE__, "set_pref"]);

sub prefs {
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

sub pref {
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

sub set_pref {
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
