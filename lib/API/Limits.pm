package API::Limits;

use strict;
use warnings;

# Limits on what a user can do, by name. Every user gets the default for
# now; limit takes the app and user so a per-user override (a user_limit
# table checked first) can be added without changing callers.

my %DEFAULT = (
  connections => 5, # connections a user can have configured
);

sub limit {
  my ($app, $user_id, $name) = @_;
  die "Unknown limit $name\n" unless exists $DEFAULT{$name};
  return $DEFAULT{$name};
}

1;
