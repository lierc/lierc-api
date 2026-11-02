use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::Routes;
use API::Controller::Connection;

# PUT /connection/:id/config saves a connection's settings without touching
# the running connection in liercd. Neither it nor PUT /connection/:id
# (save and reconnect) changes the channel list or the nick: they're set
# when the connection is created, and after that only joining and leaving
# channels, and /nick, change them.

my ($name, $captured) = API::Routes->route({ REQUEST_METHOD => "PUT", PATH_INFO => "/connection/abc/config" });
is $name, "connection.save", "PUT /connection/:id/config routes to connection.save";
is $captured->{id}, "abc", "with the connection id";
($name) = API::Routes->route({ REQUEST_METHOD => "PUT", PATH_INFO => "/connection/abc" });
is $name, "connection.edit", "PUT /connection/:id is still save and reconnect";

{
  package FakeRes;
  sub new { bless { code => $_[1] }, $_[0] }
  sub code { $_[0]{code} }
  sub decoded_content { "liercd said no" }
  package FakeApp;
  sub new {
    my ($class, $saved) = @_;
    bless { saved => $saved, updates => [], saves => [], deletes => [], notified => [], liercd => [] }, $class;
  }
  sub lookup_config { $_[0]{saved} }
  sub update_connection { my ($self, @args) = @_; push @{ $self->{updates} }, [@args] }
  sub save_connection { my ($self, @args) = @_; push @{ $self->{saves} }, [@args] }
  sub delete_connection { my ($self, @args) = @_; push @{ $self->{deletes} }, [@args] }
  sub request { my ($self, @args) = @_; push @{ $self->{liercd} }, [@args]; FakeRes->new(200) }
  sub dbh { my $self = shift; bless { app => $self }, "FakeDBH" }
  sub ok { "ok" }
  sub json { $_[1] }
  package FakeDBH;
  sub do { my ($self, $sql) = @_; push @{ $self->{app}{notified} }, $sql }
  package FakeReq;
  sub new { my ($class, $id, $body) = @_; bless { id => $id, body => $body }, $class }
  sub captures { { id => $_[0]{id} } }
  sub content { $_[0]{body} }
  sub session { { user => "u1" } }
}

my $saved = encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old", Channels => ["#a", "#key secret"] };

{
  my $app = FakeApp->new($saved);
  my $body = encode_json { Host => " 1.1.1.1 ", Port => 6697, Ssl => JSON::XS::true, Nick => "lee", Highlight => ["lee", ""], Alias => "home",
    Channels => ["#other"] };
  my $res = API::Controller::Connection::save($app, FakeReq->new("abc", $body));
  is $res, "ok", "save returns ok";
  is scalar @{ $app->{updates} }, 1, "one update";
  my ($id, $json) = @{ $app->{updates}[0] };
  is $id, "abc", "updates the connection in the URL";
  is_deeply decode_json($json),
    { Host => "1.1.1.1", Port => 6697, Ssl => JSON::XS::true, SASL => JSON::XS::false, Nick => "old", Highlight => ["lee"],
      Channels => ["#a", "#key secret"], Alias => "home" },
    "stores the cleaned config with the saved channels and nick, not the request's";
  is_deeply $app->{notified}, ["NOTIFY highlights"], "tells the notifier to reload highlights";
  is_deeply $app->{liercd}, [], "doesn't call liercd";
}

{
  my $app = FakeApp->new($saved);
  my $body = encode_json { Host => "1.1.1.1", Port => 6697, Nick => "lee" };
  API::Controller::Connection::save($app, FakeReq->new("abc", $body));
  is_deeply decode_json($app->{updates}[0][1])->{Channels}, ["#a", "#key secret"], "a request without Channels keeps them too";

  $app = FakeApp->new($saved);
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6697 }));
  is decode_json($app->{updates}[0][1])->{Nick}, "old", "a request without Nick keeps it";

  # the request's nick isn't checked either: it's ignored
  $app = FakeApp->new($saved);
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6697, Nick => "a.b" }));
  is decode_json($app->{updates}[0][1])->{Nick}, "old", "an invalid nick in the request is ignored, not refused";
}

{
  # saved channels and nick aren't re-checked: an old entry can't block an edit
  my $app = FakeApp->new(encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old.nick", Channels => ["not a channel"] });
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6667, Nick => "lee", Channels => ["bad"] }));
  is_deeply decode_json($app->{updates}[0][1])->{Channels}, ["not a channel"], "saved channels are kept as they are";
  is decode_json($app->{updates}[0][1])->{Nick}, "old.nick", "a saved nick today's rule refuses is kept";

  $app = FakeApp->new(encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old" });
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6667, Nick => "lee" }));
  is_deeply decode_json($app->{updates}[0][1])->{Channels}, [], "no saved channels: an empty list";
}

{
  my $app = FakeApp->new($saved);
  my $body = encode_json { Host => "1.1.1.1", Port => 6697, Alias => "a_b" };
  my $res = eval { API::Controller::Connection::save($app, FakeReq->new("abc", $body)) };
  like $@, qr/^Network alias can only use/, "an invalid config is refused with the rule's message";
  is_deeply $app->{updates}, [], "and nothing is saved";
  is_deeply $app->{notified}, [], "or notified";
}

{
  my $app = FakeApp->new($saved);
  my $res = eval { API::Controller::Connection::save($app, FakeReq->new("abc", "not json")) };
  like $@, qr/^Invalid connection settings/, "a body that isn't a JSON object is refused";
  is_deeply $app->{updates}, [], "and nothing is saved";
}

{
  local $ENV{DEFAULT_NETWORK_HOST};
  my $app = FakeApp->new($saved);
  my $res = eval { API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Host => "127.0.0.1", Port => 6667, Nick => "lee" })) };
  like $@, qr/private address/, "a private host is refused";
  is_deeply $app->{updates}, [], "and nothing is saved";
}

{
  # save and reconnect keeps the saved channels as well
  my $app = FakeApp->new($saved);
  my $body = encode_json { Host => "1.1.1.1", Port => 6697, Nick => "lee", Channels => ["#other"] };
  my $res = API::Controller::Connection::edit($app, FakeReq->new("abc", $body));
  is $res, "ok", "edit returns ok";
  my ($create) = grep { $_->[1] eq "abc/create" } @{ $app->{liercd} };
  ok $create, "edit restarts the connection in liercd";
  is_deeply decode_json($create->[2])->{Channels}, ["#a", "#key secret"], "with the saved channels";
  is decode_json($create->[2])->{Nick}, "old", "and nick";
  is_deeply decode_json($app->{saves}[0][2])->{Channels}, ["#a", "#key secret"], "and saves them";
}

{
  # a new connection still takes its channels from the request
  my $app = FakeApp->new(undef);
  my $body = encode_json { Host => "1.1.1.1", Port => 6697, Nick => "lee", Channels => ["#new", "#two"] };
  API::Controller::Connection::create($app, FakeReq->new(undef, $body));
  is_deeply decode_json($app->{saves}[0][2])->{Channels}, ["#new", "#two"], "create uses the request's channels";
  is decode_json($app->{saves}[0][2])->{Nick}, "lee", "and nick";

  $app = FakeApp->new(undef);
  eval { API::Controller::Connection::create($app, FakeReq->new(undef, encode_json { Host => "1.1.1.1", Port => 6697, Nick => "a.b" })) };
  like $@, qr/^Nick can only use/, "create still checks the nick";
}

done_testing;
