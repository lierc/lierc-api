use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::Limits;
use API::Controller::Connection;

# Limits are named defaults, looked up per user (so a per-user override
# can come later). A user can have at most `connections` connections:
# creating one more is refused before liercd hears of it.

is API::Limits::limit(undef, "u1", "connections"), 5, "the default connection limit is 5";
eval { API::Limits::limit(undef, "u1", "nope") };
like $@, qr/Unknown limit nope/, "an unknown limit is an error, not no limit";

{
  package FakeRes;
  sub new { bless { code => $_[1] }, $_[0] }
  sub code { $_[0]{code} }
  sub decoded_content { "liercd said no" }
  package FakeApp;
  sub notify_connection {}  # connection change announcements (t/connection_events.t)
  sub new {
    my ($class, %p) = @_;
    bless { count => 0, limited => [], log => [], liercd_code => 200, %p }, $class;
  }
  # the database's answer: the row goes in only while the user is under max
  sub save_connection_limited {
    my ($self, @args) = @_;
    push @{ $self->{limited} }, [@args];
    return 0 unless $self->{count} < $args[3];
    push @{ $self->{log} }, "insert";
    return 1;
  }
  sub delete_connection { push @{ $_[0]{log} }, "delete" }
  sub request {
    my ($self, @args) = @_;
    push @{ $self->{log} }, "liercd " . ($args[1] =~ s{.*/}{}r);
    FakeRes->new($self->{liercd_code});
  }
  sub dbh { bless {}, "FakeDBH" }
  sub json { $_[1] }
  package FakeDBH;
  sub do { 1 }
  package FakeReq;
  sub new { my ($class, $body) = @_; bless { body => $body }, $class }
  sub content { $_[0]{body} }
  sub session { { user => "u1" } }
}

my $body = encode_json { Host => "1.1.1.1", Port => 6697, Nick => "ann", Channels => ["#a"] };

{
  my $app = FakeApp->new(count => 4);
  my $res = API::Controller::Connection::create($app, FakeReq->new($body));
  is $res->{success}, "ok", "a fifth connection is created";
  is scalar @{ $app->{limited} }, 1, "through the limited save";
  is $app->{limited}[0][1], "u1", "for the user";
  is $app->{limited}[0][3], 5, "with the limit";
  is_deeply $app->{log}, ["insert", "liercd create"], "saved, then created in liercd";
}

for my $count (5, 7) {
  my $app = FakeApp->new(count => $count);
  eval { API::Controller::Connection::create($app, FakeReq->new($body)) };
  is $@, "You can have at most 5 connections\n", "with $count connections another is refused";
  is_deeply $app->{log}, [], "and liercd isn't asked";
}

{
  my $app = FakeApp->new(count => 0, liercd_code => 500);
  eval { API::Controller::Connection::create($app, FakeReq->new($body)) };
  ok $@, "a failed liercd create is an error";
  is_deeply $app->{log}, ["insert", "liercd create", "delete"], "and the row is deleted again";
}

done_testing;
