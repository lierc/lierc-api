package main;

use App;
use NSQ;
use Plack::Builder;
use Router::Boom::Method;
use Plack::App::File;
use JSON::XS;

use strict;
use warnings;

my $config = decode_json do {
  open my $fh, '<', "config.json" or die $!;
  join "", <$fh>;
};

my $app = App->new(%$config);

my $static = Plack::App::File->new(
  root => "."
)->to_app;

my $nsq = NSQ->tail(
  %{ $config->{nsq} },
  on_message => sub { $app->irc_event(@_) },
);

my $router = Router::Boom::Method->new;

$router->add( GET    => "/favicon.ico",         $app->nocontent );
$router->add( GET    => "/login.html",          "login"    );
$router->add( GET    => "/auth",                "user"     );
$router->add( POST   => "/auth",                "auth"     );
$router->add( POST   => "/register",            "register" );
$router->add( GET    => "/",                    "list"     );
$router->add( POST   => "/",                    "create"   );
$router->add( GET    => "/:id",                 "show"     );
$router->add( DELETE => "/:id",                 "delete"   );
$router->add( POST   => "/:id",                 "send"     );
$router->add( GET    => "/:id/events/:nick",    "events"   );
$router->add( GET    => "/:id/:channel/:slice", "slice"    );

builder {
  enable "Session::Cookie",
    secret => $app->secret,
    expires => 3600 * 24,
    session_key => "chats";

  sub {
    my $env = shift;

    my ($name, $captured) = $router->match(@$env{qw(REQUEST_METHOD PATH_INFO)});
    my $session = $env->{'psgix.session'};

    return $name->($env)
      if ref $name eq "CODE";

    return $app->forbidden
      unless $name =~ /^(?:auth|register)$/
        or $app->logged_in($session);

    if ($captured->{id}) {
      die "Invalid connection ID $captured->{id}"
        unless $app->verify_owner($captured->{id}, $session->{user});
    }

    return $app->handle($name, $env, $captured, $session);
  };
};
