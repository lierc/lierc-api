use strict;
use warnings;

use URI;
use URI::QueryParam;
use Plack::Builder;
use Router::Boom::Method;
use JSON::XS;

use API::Config;
use API::Stream;
use NSQ;

my $config = API::Config->new;
my $api = API::Stream->new($config->as_hash);

my $nsq = NSQ->tail(
  path       => $config->nsq_tail,
  address    => $config->nsq_address,
  topic      => "logged",
  on_message => sub { $api->irc_event(@_) },
  on_error   => sub { warn @_ },
);

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

    if ($env->{PATH_INFO} eq '/stats') {
      if ($env->{HTTP_LIERC_KEY} eq $config->key) {
        my $user = URI->new($env->{'REQUEST_URI'})->query_param("user");
        return $api->json($api->stats($user));
      }
      return $api->error("Invalid key");
    }

    sub {
      my $respond = shift;
      my $session = $env->{'psgix.session'};
      my $remote  = $env->{REMOTE_ADDR};
      my $agent   = $env->{HTTP_USER_AGENT};

      my $cv = $api->logged_in($session);

      $cv->cb(sub {
        return $respond->($api->unauthorized)
          unless $_[0]->recv;
        $api->events($session, $respond, $remote, $agent);
      });
    };
  }
};
