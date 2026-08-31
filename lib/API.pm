package API;

use strict;
use warnings;

use Class::Tiny qw(host dsn dbhost dbuser dbpass secret base secure apn imgur_key smtp_opts from_address);

use Role::Tiny::With;

with 'API::Routes';
with 'API::Dispatch';
with 'API::DB';
with 'API::Responses';
with 'API::Liercd';

1;
