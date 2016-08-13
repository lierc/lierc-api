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
  fh => *STDIN,
  on_message => sub { $app->irc_event(@_) },
);

my $router = Router::Boom::Method->new;

$router->add( GET   => "/login",               "login"   );
$router->add( GET   => "/logout",              "login"   );
$router->add( POST  => "/auth",                "auth"    );
$router->add( POST  => "/register",            "register" );
$router->add( GET   => "/",                    "index"   );
$router->add( POST  => "/create",              "create"  );
$router->add( GET   => "/:id",                 "chat"    );
$router->add( GET   => "/:id/destroy",         "destroy" );
$router->add( GET   => "/:id/events/:nick",    "events"  );
$router->add( POST  => "/:id/raw",             "raw"     );
$router->add( GET   => "/:id/status",          "status"  );
$router->add( GET   => "/:id/:channel/:slice", "slice"   );
$router->add( GET   => "/static/*",            $static   );
$router->add( GET   => "/favicon.ico",         $app->nocontent );

builder {
  enable "CrossOrigin", origins => "*";
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

    return $app->redirect("/login")
      unless $name =~ /^(?:login|auth|register)$/
        or $app->logged_in($session);

    if ($captured->{id}) {
      die "Invalid connection ID"
        unless $app->verify_owner($captured->{id}, $session->{user});
    }

    return $app->handle($name, $env, $captured, $session);
  };
};
