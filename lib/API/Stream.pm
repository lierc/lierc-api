package API::Stream;

use strict;
use warnings;

use Class::Tiny qw(host dsn dbhost dbuser dbpass secret base secure);

use Role::Tiny::With;

with 'API::Responses';
with 'API::Stream::DB';
with 'API::Stream::Events';
with 'API::Stream::Liercd';

1;
