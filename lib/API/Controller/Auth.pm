package API::Controller::Auth;

use parent 'API::Controller';

use Util;
use Data::Validate::Email;
use MIME::Lite;
use Text::Xslate;

API->register("auth.show",     __PACKAGE__);
API->register("auth.login",    __PACKAGE__);
API->register("auth.register", __PACKAGE__);
API->register("auth.logout",   __PACKAGE__);
API->register("auth.token",    __PACKAGE__);
API->register("auth.verify",   __PACKAGE__);

my $tx = Text::Xslate->new;
my $auth_template = q{
Hello <: $name :>,

Thank you for signing up with relaychat.party!

Please click the following link to validate your email address:
<: $link :>

You must visit this link in the next day, or your account will
be deleted from the system.

Happy chatting!
};

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

  my ($id, $token) = $app->add_user($user, $email, $pass);
  $req->session->{user} = $id;

  my $data = $tx->render_string(
    $auth_template,
    {
      name => $user,
      link => "https://relaychat.party/api/verify?$token",
    }
  );

  my $msg = MIME::Lite->new(
    From    => 'no-reply@relaychat.party',
    To      => $email,
    Subject => 'Please verify your relaychat.party account',
    Type    => 'text/plain',
    Data    => $data,
  );
  $msg->send('smtp');

  return $app->handle("auth.show", $req);
}

sub verify {
  my ( $app, $req ) = @_;
  my $query = $req->query_string;

  my $row = $app->dbh->selectcol_arrayref('SELECT id FROM "user" WHERE verify_token=?', {}, $query);

  return $app->unauthorized unless $row;

  $app->dbh->do('UPDATE "user" SET verified=true WHERE id=?', {}, $row->[0]);
  $app->ok;
}

sub token {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $token = $app->get_token($user);
  $app->json({
    token => $token,
    extra => [ map $app->get_token($user), (0 .. 5) ],
  });
}

1;
