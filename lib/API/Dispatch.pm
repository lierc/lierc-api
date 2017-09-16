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

  return $self->unauthorized
    unless ($name && $name =~ /^auth\.(?:login|register|logout)$/)
      or ($name && $name =~ /^apn\.(?:package|log|(?:un)?register)$/)
      or $self->logged_in($req->session);

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
