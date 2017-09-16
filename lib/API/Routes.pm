package API::Routes;

use Role::Tiny;
use Router::Boom::Method;

my $router = Router::Boom::Method->new;

$router->add( GET    => "/auth",                "auth.show" );
$router->add( POST   => "/auth",                "auth.login" );
$router->add( POST   => "/register",            "auth.register" );
$router->add( undef  ,  "/logout",              "auth.logout" );
$router->add( GET    => "/token",               "auth.token" );

$router->add( GET    => "/log/:event",          "message.log");
$router->add( GET    => "/missed",              "message.missed");
$router->add( GET    => "/seen",                "message.seen");

$router->add( GET    => "/preference",          "pref.list" );
$router->add( GET    => "/preference/:pref",    "pref.show" );
$router->add( POST   => "/preference/:pref",    "pref.upsert" );

$router->add( GET    => "/connection",          "connection.list" );
$router->add( POST   => "/connection",          "connection.create" );
$router->add( GET    => "/connection/:id",      "connection.show" );
$router->add( PUT    => "/connection/:id",      "connection.edit" );
$router->add( DELETE => "/connection/:id",      "connection.delete" );
$router->add( POST   => "/connection/:id",      "connection.send" );

$router->add( DELETE => "/connection/:id/nick/:nick", "private.delete" );
$router->add( GET    => "/privates",                  "private.list" );

$router->add( GET    => "/connection/:id/channel/:channel/events",        "channel.logs" );
$router->add( GET    => "/connection/:id/channel/:channel/events/:event", "channel.logs_id" );
$router->add( POST   => "/connection/:id/channel/:channel/seen",          "channel.set_seen" );
$router->add( GET    => "/connection/:id/channel/:channel/last",          "channel.last" );

$router->add( POST   => "/connection/:id/channel/:channel/ignore",        "ignore.create" );
$router->add( DELETE => "/connection/:id/channel/:channel/ignore/:from",  "ignore.delete" );
$router->add( GET    => "/ignore",                                        "ignore.list" );

$router->add( GET    => "/notification/web_push_keys",          "webpush.keys" );
$router->add( GET    => "/notification/web_push",               "webpush.list" );
$router->add( POST   => "/notification/web_push",               "webpush.upsert" );
$router->add( DELETE => "/notification/web_push/{endpoint:.+}", "webpush.delete" );

$router->add( GET    => "/notification/apn/config",             "apn.config" );
$router->add( POST   => "/notification/apn/device",             "apn.device" );
$router->add( POST   => "/v1/pushPackages/:push_id",            "apn.package" );
$router->add( POST   => "/v1/log",                              "apn.log" );
$router->add( POST   => "/v1/devices/:device_id/registrations/:push_id", "apn.register" );
$router->add( DELETE => "/v1/devices/:device_id/registrations/:push_id", "apn.unregister" );

sub route {
  my $self = shift;
  my $env = shift;

  my ($name, $captured) = $router->match(@$env{qw(REQUEST_METHOD PATH_INFO)});

  return ($name, $captured);
}

1;
