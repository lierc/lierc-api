use strict;
use warnings;

use URI;
use URI::QueryParam;
use Plack::Builder;
use Router::Boom::Method;
use JSON::XS;

use API::Config;
use API::Async;
use NSQ;

my $config = API::Config->new;
my $api = API::Async->new($config->as_hash);

my $nsq = NSQ->tail(
  path       => $config->nsq_tail,
  address    => $config->nsq_address,
  topic      => "logged",
  on_message => sub { $api->irc_event(@_) },
  on_error   => sub { warn @_ },
);

my $connect = NSQ->tail(
  path       => $config->nsq_tail,
  address    => $config->nsq_address,
  topic      => "connect",
  on_message => sub { $api->connect_event(@_) },
  on_error   => sub { warn @_ },
);


builder {
  enable_if { $_[0]->{REMOTE_ADDR} eq '127.0.0.1' }
    "Plack::Middleware::ReverseProxy";

  enable "Session::Cookie",
    secret => $api->secret,
    expires => 3600 * 24 * 7,
    httponly => 1,
    session_key => "chats";

  sub {
    my $env = shift;

    sub {
      my $respond = shift;
      my $session = $env->{'psgix.session'};
      my $params = URI->new($env->{'REQUEST_URI'})->query_form_hash;

      my $cv = $api->logged_in($session);

      $cv->cb(sub {
        my $logged_in = $_[0]->recv;
        return $respond->($api->unauthorized) unless $logged_in;
        $api->events($session, $respond, $params);
      });
    };
  }
};
