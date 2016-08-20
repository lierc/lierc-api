use strict;
use warnings;

use Plack::Builder;
use Router::Boom::Method;
use JSON::XS;

use API::Async;
use NSQ;

my $config = decode_json do {
  open my $fh, '<', "config.json" or die $!;
  join "", <$fh>;
};

my $api = API::Async->new(%$config);

my $nsq = NSQ->tail(
  %{ $config->{nsq} },
  on_message => sub { $api->irc_event(@_) },
  on_error   => sub { warn @_ },
);

builder {
  enable_if { $_[0]->{REMOTE_ADDR} eq '127.0.0.1' }
    "Plack::Middleware::ReverseProxy";

  enable "Session::Cookie",
    secret => $api->secret,
    expires => 3600 * 24,
    httponly => 1,
    session_key => "chats";

  sub {
    my $env = shift;

    sub {
      my $respond = shift;
      my $session = $env->{'psgix.session'};

      my $cv = $api->logged_in($session);

      $cv->cb(sub {
        my $logged_in = $_[0]->recv;
        return $respond->($api->unauthorized) unless $logged_in;
        $api->events($session, $respond);
      });
    };
  }
};
