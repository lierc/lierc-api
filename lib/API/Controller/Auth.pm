package API::Controller::Auth;

use parent 'API::Controller';

use Util;
use Data::Validate::Email;

API->register("auth.show",     __PACKAGE__);
API->register("auth.login",    __PACKAGE__);
API->register("auth.register", __PACKAGE__);
API->register("auth.logout",   __PACKAGE__);
API->register("auth.token",    __PACKAGE__);

sub show {
  my ($app, $req) = @_;
  my $user = $app->lookup_user($req->session->{user});
  return $app->json({
    email => $user->{email},
    user => $user->{username},
    id => $user->{id},
  });
}

sub login {
  my ($app, $req) = @_;

  my $pass  = $req->parameters->{pass};
  my $email = $req->parameters->{email};
  my $hashed = Util->hash_password($pass, $app->secret);

  my $sth = $app->dbh->prepare_cached(
    q{SELECT id FROM "user" WHERE (email=? OR username=?) AND password=?}
  );
  $sth->execute($email, $email, $hashed);
  my $row = $sth->fetchrow_arrayref;
  $sth->finish;

  if ($row) {
    $req->session->{user} = $row->[0];
    return $app->handle("auth.show", $req);
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

  for (qw(username email pass)) {
    die "$_ is required"
      unless defined $req->parameters->{$_}
        && $req->parameters->{$_} =~ /\S/;
  }

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};
  my $user = $req->parameters->{username};

  die "Invalid email address"
    unless Data::Validate::Email::is_email($email);

  my $id = $app->add_user($user, $email, $pass);
  $req->session->{user} = $id;

  return $app->handle("auth.show", $req);
}

sub token {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $token = $app->get_token($user);
  $app->json({token => $token});
}

1;
