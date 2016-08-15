package API;

use strict;
use warnings;

use Class::Tiny qw(host dsn dbuser dbpass secret base);

use Role::Tiny::With;

with 'API::Routes';
with 'API::DB';
with 'API::Responses';
with 'API::Liercd';

1;
