package API::Controller::Auth;

use parent 'API::Controller';

use Data::Validate::Email;

API->register("auth.show",     [__PACKAGE__, "show"]);
API->register("auth.login",    [__PACKAGE__, "login"]);
API->register("auth.register", [__PACKAGE__, "register"]);
API->register("auth.logout",   [__PACKAGE__, "logout"]);

sub show {
  my ($self, $req, $captures, $session) = @_;
  my $user = $self->lookup_user($session->{user});
  return $self->json({
    email => $user->{email},
    user => $user->{id},
  });
}

sub login {
  my ($self, $req, $captures, $session) = @_;

  my $pass  = $req->parameters->{pass};
  my $email = $req->parameters->{email};
  my $hashed = Util->hash_password($pass, $self->secret);

  my ($row) = $self->dbh->selectall_array(
    q{SELECT id FROM "user" WHERE email=? AND password=?},
    {}, $email, $hashed
  );

  if ($row) {
    $req->env->{'psgix.session'}->{user} = $row->[0];
    return $self->ok;
  }

  return $self->unauthorized("Invalid email or password");
}

sub logout {
  my ($self, $req, $captures) = @_;
  delete $req->env->{'psgix.session'}->{user};
  return $self->ok;
}

sub register {
  my ($self, $req, $captures, $session) = @_;

  for (qw(email pass)) {
    die "$_ is required"
      unless defined $req->parameters->{$_}
        && $req->parameters->{$_} =~ /\S/;
  }

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};

  die "Invalid email address"
    unless Data::Validate::Email::is_email($email);

  my $id = $self->add_user($email, $pass);
  $session->{user} = $id;
  return $self->ok;
}

1;
