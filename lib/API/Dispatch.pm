package API::Dispatch;

use Role::Tiny;

our %actions;

sub register {
  my ($class, $action, $handler) = @_;
  my ($package, $method) = @$handler;
  $actions{$action} = sub {
    no strict "refs";
    &{"$package\::$method"}(@_);
  };
}

sub handle {
  my ($self, $name, $env, $captured, $session) = @_;
  if (my $handler = $name && $actions{$name}) {
    my $req = Plack::Request->new($env);

    my ($res, $err);
    {
      local $@;
      $res = eval { $handler->($self, $req, $captured, $session) };
      $err = $@;
    }

    return $self->error($err) if $err;
    return $res;
  }

  return $self->not_found;
}

1;
