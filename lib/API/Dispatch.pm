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

sub handle {
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
