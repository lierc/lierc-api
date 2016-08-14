package API;

use strict;
use warnings;

use JSON::XS;
use AnyEvent;
use Role::Tiny::With;

use Class::Tiny qw(host dsn dbuser dbpass secret base);

with 'API::Routes';
with 'API::DB';
with 'API::Responses';
with 'API::Liercd';
with 'API::Events';

sub BUILD {
  my $self = shift;
  $self->start_pings;
}

1;
