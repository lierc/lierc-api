package API::Controller::Auth;

use parent 'API::Controller';

use Data::Validate::Email;

API->register("auth.show",     [__PACKAGE__, "show"]);
API->register("auth.login",    [__PACKAGE__, "login"]);
API->register("auth.register", [__PACKAGE__, "register"]);
API->register("auth.logout",   [__PACKAGE__, "logout"]);

sub show {
  my ($app, $req) = @_;
  my $user = $app->lookup_user($req->session->{user});
  return $app->json({
    email => $user->{email},
    user => $user->{id},
  });
}

sub login {
  my ($app, $req) = @_;

  my $pass  = $req->parameters->{pass};
  my $email = $req->parameters->{email};
  my $hashed = Util->hash_password($pass, $app->secret);

  my $sth = $app->dbh->prepare_cached(
    q{SELECT id FROM "user" WHERE email=? AND password=?}
  );
  $sth->execute($email, $hashed);
  my $row = $sth->fetchrow_arrayref;
  $sth->finish;

  if ($row) {
    $req->env->{'psgix.session'}->{user} = $row->[0];
    return $app->ok;
  }

  return $app->unauthorized("Invalid email or password");
}

sub logout {
  my ($app, $req) = @_;
  delete $req->env->{'psgix.session'}->{user};
  return $app->ok;
}

sub register {
  my ($app, $req) = @_;

  for (qw(email pass)) {
    die "$_ is required"
      unless defined $req->parameters->{$_}
        && $req->parameters->{$_} =~ /\S/;
  }

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};

  die "Invalid email address"
    unless Data::Validate::Email::is_email($email);

  my $id = $app->add_user($email, $pass);
  $req->session->{user} = $id;
  return $app->ok;
}

1;
