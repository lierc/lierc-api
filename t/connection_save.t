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
  sub update_connection { my ($self, @args) = @_; push @{ $self->{updates} }, [@args]; push @{ $self->{log} }, "update" }
  sub save_connection { my ($self, @args) = @_; push @{ $self->{saves} }, [@args]; push @{ $self->{log} }, "insert" }
  sub delete_connection { my ($self, @args) = @_; push @{ $self->{deletes} }, [@args]; push @{ $self->{log} }, "delete" }
  sub request {
    my ($self, @args) = @_;
    push @{ $self->{liercd} }, [@args];
    push @{ $self->{log} }, "liercd " . ($args[1] =~ s{.*/}{}r);
    FakeRes->new($self->{liercd_code}{ $args[1] =~ s{.*/}{}r } // 200);
  }
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
  is_deeply decode_json($app->{updates}[0][1])->{Channels}, ["#a", "#key secret"], "and saves them";
}

{
  # the stream server finds who to send a connection's events to by its
  # row, so save and reconnect updates the row in place: deleting and
  # reinserting it lost the CREATE event, and the UI never saw the
  # connection come back
  my $app = FakeApp->new($saved);
  API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { Host => "2.2.2.2", Port => 6697 }));
  is_deeply $app->{log}, ["liercd destroy", "update", "liercd create"],
    "edit stops the connection, updates its row, then starts it";
  is $app->{updates}[0][0], "abc", "updating the connection in the URL";
  is decode_json($app->{updates}[0][1])->{Host}, "2.2.2.2", "with the new settings";

  # a connection liercd had already lost is still started
  $app = FakeApp->new($saved);
  $app->{liercd_code}{destroy} = 404;
  API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { Host => "2.2.2.2", Port => 6697 }));
  is_deeply $app->{log}, ["liercd destroy", "update", "liercd create"], "even if liercd wasn't running it";
}

{
  # save and reconnect only reconnects if a setting the IRC connection
  # uses changed: address, port, username, password, TLS or SASL
  my $server = encode_json { Host => "1.1.1.1", Port => "6697", Nick => "old", User => "u", Pass => "p",
    Ssl => JSON::XS::true, SASL => JSON::XS::false, Channels => ["#a"], Highlight => ["x"] };
  my %same = (Host => " 1.1.1.1 ", Port => 6697, User => "u", Pass => "p", Ssl => 1, SASL => 0);

  my $app = FakeApp->new($server);
  my $res = API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json {
    %same, Nick => "other", Channels => ["#b"], Highlight => ["y"], Alias => "home" }));
  is $res, "ok", "unchanged server settings: edit returns ok";
  is_deeply $app->{log}, ["update"], "and only updates the row, without reconnecting";
  my $got = decode_json($app->{updates}[0][1]);
  is_deeply [ @$got{qw(Alias Highlight Nick Channels)} ], ["home", ["y"], "old", ["#a"]],
    "saving the alias and highlights, keeping the nick and channels";
  is_deeply $app->{notified}, ["NOTIFY highlights"], "and telling the notifier";

  for my $change ([Host => "8.8.8.8"], [Port => 6698], [User => "v"], [User => ""],
                  [Pass => "q"], [Pass => ""], [Ssl => 0], [SASL => 1]) {
    my ($field, $value) = @$change;
    my $app = FakeApp->new($server);
    API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { %same, $field => $value }));
    is_deeply $app->{log}, ["liercd destroy", "update", "liercd create"],
      "changing $field to '$value' reconnects";
  }

  # no username or password saved, and none given
  $app = FakeApp->new(encode_json { Host => "1.1.1.1", Port => 6697, Nick => "old" });
  API::Controller::Connection::edit($app, FakeReq->new("abc", encode_json { Host => "1.1.1.1", Port => 6697, User => "", Pass => "" }));
  is_deeply $app->{log}, ["update"], "blank username and password match none saved";
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
