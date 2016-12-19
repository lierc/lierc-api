package API;

use strict;
use warnings;

use Class::Tiny qw(host dsn dbhost dbuser dbpass secret base);

use Role::Tiny::With;

with 'API::Dispatch';
with 'API::DB';
with 'API::Responses';
with 'API::Liercd';

1;
