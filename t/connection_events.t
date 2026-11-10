use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::Controller::Connection;

# Every change to a connection row is announced on the connections
# channel (notify_connection), so the stream server can tell the owner's
# open tabs: create as soon as the row exists (before liercd connects), update on both edit
# paths and on save, delete once the row is gone. Nothing is announced for
# a change that didn't happen.

{
  package FakeRes;
  sub new { bless { code => $_[1] }, $_[0] }
  sub code { $_[0]{code} }
  sub decoded_content { "liercd said no" }
  package FakeApp;
  sub new {
    my ($class, $saved, %codes) = @_;
    bless { saved => $saved, codes => \%codes, events => [], log => [] }, $class;
  }
  sub lookup_config { $_[0]{saved} }
  sub update_connection { push @{ $_[0]{log} }, "update" }
  sub save_connection_limited { push @{ $_[0]{log} }, "insert"; !$_[0]{full} }
  sub delete_connection { push @{ $_[0]{log} }, "delete" }
  sub request {
    my ($self, $method, $path) = @_;
    my $cmd = $path =~ s{.*/}{}r;
    push @{ $self->{log} }, "liercd $cmd";
    FakeRes->new($self->{codes}{$cmd} // 200);
  }
  sub notify_connection {
    my ($self, @args) = @_;
    push @{ $self->{events} }, [@args];
    push @{ $self->{log} }, "notify $args[2]";
  }
  sub dbh { bless {}, "FakeDBH" }
  sub ok { "ok" }
  sub json { $_[1] }
  package FakeDBH;
  sub do {}
  package FakeReq;
  sub new { my ($class, $id, $body) = @_; bless { id => $id, body => $body }, $class }
  sub captures { { id => $_[0]{id} } }
  sub content { $_[0]{body} }
  sub session { { user => "u1" } }
}

my $saved = encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old", Channels => ["#a"] };
my $cfg = { Host => "1.1.1.1", Port => 6667, Nick => "lee" };

{
  my $app = FakeApp->new($saved);
  API::Controller::Connection::start($app, "c1", "u1", $cfg, 5);
  is_deeply $app->{events}, [["u1", "c1", "create"]], "a created connection is announced";
  is_deeply $app->{log}, ["insert", "notify create", "liercd create"],
    "before liercd connects, so tabs have it before its first status";
}

{
  my $app = FakeApp->new($saved, create => 500);
  eval { API::Controller::Connection::start($app, "c1", "u1", $cfg, 5) };
  ok $@, "a failed create dies";
  is_deeply $app->{events}, [["u1", "c1", "create"], ["u1", "c1", "delete"]],
    "and announces the connection deleted again, as its row is";
  is_deeply $app->{log}, ["insert", "notify create", "liercd create", "delete", "notify delete"], "in that order";
}

{
  my $app = FakeApp->new($saved);
  $app->{full} = 1;
  eval { API::Controller::Connection::start($app, "c1", "u1", $cfg, 5) };
  like $@, qr/at most 5 connections/, "a create over the limit is refused";
  is_deeply $app->{events}, [], "and announces nothing";
}

{
  my $app = FakeApp->new($saved);
  API::Controller::Connection::delete($app, FakeReq->new("c1"));
  is_deeply $app->{events}, [["u1", "c1", "delete"]], "a deleted connection is announced";
  is_deeply $app->{log}, ["liercd destroy", "delete", "notify delete"], "after its row is gone";
}

{
  my $app = FakeApp->new($saved, destroy => 500);
  eval { API::Controller::Connection::delete($app, FakeReq->new("c1")) };
  ok $@, "a delete liercd refuses dies";
  is_deeply $app->{events}, [], "and announces nothing";
}

{
  # Save & Reconnect that changes the server settings: destroy, save, create
  my $app = FakeApp->new($saved);
  API::Controller::Connection::edit($app, FakeReq->new("c1", encode_json { Host => "1.1.1.1", Port => 6697, Nick => "lee" }));
  is_deeply $app->{events}, [["u1", "c1", "update"]], "a reconnecting edit is announced as an update";
  is $app->{log}[-1], "notify update", "once the connection is recreated";
}

{
  # Save & Reconnect with nothing that needs a reconnect: just saved
  my $app = FakeApp->new($saved);
  API::Controller::Connection::edit($app, FakeReq->new("c1", encode_json { Host => "1.1.1.1", Port => 6667, Nick => "old", Alias => "home" }));
  is_deeply $app->{events}, [["u1", "c1", "update"]], "an edit that only saves is an update too";
  ok !grep({ /^liercd/ } @{ $app->{log} }), "without touching liercd";
}

{
  my $app = FakeApp->new($saved);
  API::Controller::Connection::save($app, FakeReq->new("c1", encode_json { Alias => "home", Highlight => [] }));
  is_deeply $app->{events}, [["u1", "c1", "update"]], "save (alias and highlights) is an update";
}

{
  my $app = FakeApp->new($saved);
  eval { API::Controller::Connection::save($app, FakeReq->new("c1", encode_json { Alias => "a_b" })) };
  ok $@, "an invalid save dies";
  is_deeply $app->{events}, [], "and announces nothing";
}

done_testing;
