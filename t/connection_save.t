use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::Routes;
use API::Controller::Connection;

# PUT /connection/:id/config saves a connection's highlights and alias
# without touching the running connection in liercd. PUT /connection/:id
# (save and reconnect) saves the rest, but not the channel list or the nick:
# they're set when the connection is created, and after that only joining
# and leaving channels, and /nick, change them.

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
  # Save changes only highlights and the alias: the rest of the request is
  # ignored and the rest of the saved config kept as it is
  my $app = FakeApp->new($saved);
  my $body = encode_json { Host => "2.2.2.2", Port => 6697, Ssl => JSON::XS::true, Nick => "lee", Pass => "new",
    Highlight => ["lee", " pizza ", ""], Alias => " home ", Channels => ["#other"] };
  my $res = API::Controller::Connection::save($app, FakeReq->new("abc", $body));
  is $res, "ok", "save returns ok";
  is scalar @{ $app->{updates} }, 1, "one update";
  my ($id, $json) = @{ $app->{updates}[0] };
  is $id, "abc", "updates the connection in the URL";
  is_deeply decode_json($json),
    { Host => "1.1.1.1", Port => 6667, Nick => "old", Channels => ["#a", "#key secret"], Highlight => ["lee", "pizza"], Alias => "home" },
    "stores the saved config with the request's highlights and alias, cleaned";
  is_deeply $app->{notified}, ["NOTIFY highlights"], "tells the notifier to reload highlights";
  is_deeply $app->{liercd}, [], "doesn't call liercd";
}

{
  my $with_alias = encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old", Alias => "home", Highlight => ["old"] };
  my $app = FakeApp->new($with_alias);
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Highlight => [], Alias => "  " }));
  my $got = decode_json($app->{updates}[0][1]);
  ok !exists $got->{Alias}, "a blank alias removes it";
  is_deeply $got->{Highlight}, [], "and no highlight words is an empty list";

  $app = FakeApp->new($with_alias);
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json {}));
  $got = decode_json($app->{updates}[0][1]);
  ok !exists $got->{Alias}, "a request without Alias removes it too";
}

{
  # the saved settings aren't re-checked, nor the host resolved: an old
  # value or a DNS failure can't block saving highlights
  local $ENV{DEFAULT_NETWORK_HOST};
  my $app = FakeApp->new(encode_json { Host => "127.0.0.1", Port => 6667, Nick => "old.nick", User => "bad user", Channels => ["not a channel"] });
  API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Highlight => ["x"], Alias => "net" }));
  is_deeply decode_json($app->{updates}[0][1]),
    { Host => "127.0.0.1", Port => 6667, Nick => "old.nick", User => "bad user", Channels => ["not a channel"], Highlight => ["x"], Alias => "net" },
    "saved settings are kept as they are";
}

{
  my $app = FakeApp->new($saved);
  my $res = eval { API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Alias => "a_b" })) };
  like $@, qr/^Network alias can only use/, "an invalid alias is refused with the rule's message";
  $res = eval { API::Controller::Connection::save($app, FakeReq->new("abc", encode_json { Highlight => ["a\x07b"] })) };
  like $@, qr/^Highlight words can't contain control characters/, "so are invalid highlight words";
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
  # an edit that reconnects still checks the whole config
  my $app = FakeApp->new($saved);
  my $res = eval { API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6697, Alias => "a_b" })) };
  like $@, qr/^Network alias can only use/, "save and reconnect refuses an invalid config";
  is_deeply $app->{liercd}, [], "without touching liercd";
}

{
  local $ENV{DEFAULT_NETWORK_HOST};
  my $app = FakeApp->new($saved);
  my $res = eval { API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { Host => "127.0.0.1", Port => 6667 })) };
  like $@, qr/private address/, "and a private host";
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
