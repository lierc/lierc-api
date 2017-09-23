use strict;
use warnings;

use Plack::Builder;
use API;
use API::Bootstrap;
use API::Config;

my $config = API::Config->new;
my $api    = API->new($config->as_hash);

builder {
  enable_if { $_[0]->{REMOTE_ADDR} eq '127.0.0.1' }
    "Plack::Middleware::ReverseProxy";

  enable "Session::Cookie",
    secret      => $api->secret,
    expires     => 3600 * 24 * 7,
    httponly    => 1,
    secure      => $api->secure,
    session_key => "chats";

  enable "AccessLog";

  sub {
    my $env = shift;
    my $session = $env->{'psgix.session'};
    $api->dispatch($env, $session);
  };
};
