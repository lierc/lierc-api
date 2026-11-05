package API::Dispatch;

use Role::Tiny;
use API::Request;

our %actions;

sub register {
  my ($class, $action, $package) = @_;
  my ($controller, $method) = split /\./, $action, 2;
  $actions{$action} = sub {
    no strict "refs";
    &{"$package\::$method"}(@_);
  };
}

sub dispatch {
  my ($self, $env, $session) = @_;
  my ($name, $captured) = $self->route($env);
  my $req = API::Request->new($env, $captured, $session);
  return $self->handle($name, $req);
}

sub handle {
  my ($self, $name, $req) = @_;

  # Allowed logged out: logging in and signing up, and the emailed links.
  # verify, reset and reset_confirm find their user by the signed token in
  # the request body, so they don't need (or use) a session; a link may be
  # opened logged out, or forwarded to someone else.
  my $user = $self->logged_in($req->session);
  return $self->unauthorized
    unless ($name && $name =~ /^auth\.(?:login|register|logout|verify|reset|reset_confirm)$/)
      or $user;

  # A signup that hasn't confirmed its email yet (verified false) gets only
  # what the check-your-email page needs. NULL is an account from before
  # signups had to verify, and is let in.
  return $self->unverified
    if $user && defined $user->{verified} && !$user->{verified}
      && !($name && $name =~ /^auth\.(?:show|logout|resend_verify|login|register|verify|reset|reset_confirm)$/);

  if ($req->captures->{id}) {
    return $self->unauthorized("Invalid connection id '$req->captures->{id}'")
    unless $self->verify_owner($req->captures->{id}, $req->session->{user});
  }

  return $self->run($name, $req);
}

sub run {
  my ($self, $name, $req) = @_;
  if (my $handler = $name && $actions{$name}) {
    my ($res, $err);
    {
      local $@;
      $res = eval { $handler->($self, $req) };
      $err = $@;
    }

    if ($err) {
      warn "Error handing $name: '$err'\n";
      return $self->error($err);
    }

    return $res;
  }

  return $self->not_found;
}

1;
