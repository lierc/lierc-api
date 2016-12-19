package API::Async;

use strict;
use warnings;

use Class::Tiny qw(host dsn dbhost dbuser dbpass secret base);

use Role::Tiny::With;

with 'API::Responses';
with 'API::Async::DB';
with 'API::Async::Events';
with 'API::Async::Liercd';

1;
