use strict;
use warnings;

use Plack::Builder;
use Router::Boom::Method;
use Plack::App::File;
use JSON::XS;

use API;

my $config = decode_json do {
  open my $fh, '<', "config.json" or die $!;
  join "", <$fh>;
};

my $api    = API->new(%$config);
my $router = Router::Boom::Method->new;

$router->add( GET    => "/auth",                "user"     );
$router->add( POST   => "/auth",                "auth"     );
$router->add( POST   => "/register",            "register" );
$router->add( undef  ,  "/logout",              "logout"   );

$router->add( GET    => "/unread/:event",       "unread"   );
$router->add( GET    => "/privates",            "privates" );

$router->add( GET    => "/preference",          "prefs"    );
$router->add( GET    => "/preference/:pref",    "pref"     );
$router->add( POST   => "/preference/:pref",    "set_pref" );

$router->add( GET    => "/connection",          "list"     );
$router->add( POST   => "/connection",          "create"   );
$router->add( GET    => "/connection/:id",      "show"     );
$router->add( PUT    => "/connection/:id",      "edit"     );
$router->add( DELETE => "/connection/:id",      "delete"   );
$router->add( POST   => "/connection/:id",      "send"     );

$router->add( GET    => "/connection/:id/channel/:channel/events",        "logs" );
$router->add( GET    => "/connection/:id/channel/:channel/events/:event", "logs_id" );

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

    my ($name, $captured) = $router->match(@$env{qw(REQUEST_METHOD PATH_INFO)});
    my $session = $env->{'psgix.session'};

    return $api->unauthorized
      unless ($name && $name =~ /^(?:auth|register|logout)$/)
        or $api->logged_in($session);

    if ($captured->{id}) {
      return $api->unauthorized("Invalid connection id '$captured->{id}'")
        unless $api->verify_owner($captured->{id}, $session->{user});
    }

    return $api->handle($name, $env, $captured, $session);
  };
};
