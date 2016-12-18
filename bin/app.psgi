use strict;
use warnings;

use Plack::Builder;
use Router::Boom::Method;
use JSON::XS;

use API;

use API::Controller::Auth;
use API::Controller::Pref;
use API::Controller::Message;
use API::Controller::Connection;
use API::Controller::Channel;
use API::Config;

my $config = API::Config->new;
my $api    = API->new($config->as_hash);
my $router = Router::Boom::Method->new;

$router->add( GET    => "/auth",                "auth.show" );
$router->add( POST   => "/auth",                "auth.login" );
$router->add( POST   => "/register",            "auth.register" );
$router->add( undef  ,  "/logout",              "auth.logout" );

$router->add( GET    => "/missed",              "message.missed");
$router->add( GET    => "/seen",                "message.seen");
$router->add( GET    => "/privates",            "message.privates");

$router->add( GET    => "/preference",          "pref.list" );
$router->add( GET    => "/preference/:pref",    "pref.show" );
$router->add( POST   => "/preference/:pref",    "pref.upsert" );

$router->add( GET    => "/connection",          "connection.list" );
$router->add( POST   => "/connection",          "connection.create" );
$router->add( GET    => "/connection/:id",      "connection.show" );
$router->add( PUT    => "/connection/:id",      "connection.edit" );
$router->add( DELETE => "/connection/:id",      "connection.delete" );
$router->add( POST   => "/connection/:id",      "connection.send" );

$router->add( GET    => "/connection/:id/channel/:channel/events",        "channel.logs" );
$router->add( GET    => "/connection/:id/channel/:channel/events/:event", "channel.logs_id" );
$router->add( POST   => "/connection/:id/channel/:channel/seen",          "channel.set_seen" );

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

    my ($name, $captured) = $router->match(@$env{qw(REQUEST_METHOD PATH_INFO)});
    my $session = $env->{'psgix.session'};

    return $api->unauthorized
      unless ($name && $name =~ /^auth\.(?:login|register|logout)$/)
        or $api->logged_in($session);

    if ($captured->{id}) {
      return $api->unauthorized("Invalid connection id '$captured->{id}'")
        unless $api->verify_owner($captured->{id}, $session->{user});
    }

    return $api->handle($name, $env, $captured, $session);
  };
};
