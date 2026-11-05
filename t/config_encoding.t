use strict;
use warnings;
use utf8;

use Test::More;
use JSON::XS;
use API::StoredJSON;
use API::Controller::Connection;
use API::Controller::Auth;

# Connection configs go to Postgres as text (characters): DBD::Pg encodes
# what it is given, so UTF-8 bytes from encode_json were stored encoded
# twice and Go readers (the logger's highlights, lctl, revive) saw "ð°"
# for 🍰. Request bodies and what's sent to liercd stay UTF-8 bytes.

my $cake  = "lee\x{1F370}";
my $bytes = "\xF0\x9F\x8D\xB0";   # 🍰 as UTF-8

is API::StoredJSON::decode(API::StoredJSON::encode({ Highlight => [$cake] }))->{Highlight}[0], $cake,
  "stored JSON round-trips characters";
like API::StoredJSON::encode({ Highlight => [$cake] }), qr/\x{1F370}/, "and is a character string, not bytes";

{
  package FakeRes;
  sub new { bless { code => $_[1] }, $_[0] }
  sub code { $_[0]{code} }
  sub decoded_content { "no" }
  package FakeApp;
  sub new { my ($class, $saved) = @_; bless { saved => $saved, stored => [], liercd => [] }, $class }
  sub lookup_config { $_[0]{saved} }
  sub update_connection { push @{ $_[0]{stored} }, $_[2] }
  sub save_connection { push @{ $_[0]{stored} }, $_[3] }
  sub save_connection_limited { push @{ $_[0]{stored} }, $_[3]; 1 }
  sub delete_connection {}
  sub request { my ($self, @args) = @_; push @{ $self->{liercd} }, [@args]; FakeRes->new(200) }
  sub dbh { bless {}, "FakeDBH" }
  sub default_network { { Host => "irc.example.com", Port => 6697, Alias => "r\x{e9}seau \x{1F370}" } }
  sub ok { "ok" }
  sub json { $_[1] }
  package FakeDBH;
  sub do { 1 }
  package FakeReq;
  sub new { my ($class, $id, $body) = @_; bless { id => $id, body => $body }, $class }
  sub captures { { id => $_[0]{id} } }
  sub content { $_[0]{body} }
  sub session { { user => "u1" } }
}

my %base = (Host => "1.1.1.1", Port => 6667, Nick => "lee", Channels => ["#a"]);
my $body = encode_json { %base, Highlight => [$cake] };   # as the UI sends it: UTF-8 bytes
my $saved = API::StoredJSON::encode({ %base, Highlight => [$cake] });

sub stored_ok {
  my ($app, $what) = @_;
  my $s = $app->{stored}[-1];
  ok defined $s && index($s, "\x{1F370}") >= 0, "$what stores the highlight as characters"
    or diag explain $s;
}
sub liercd_ok {
  my ($app, $what) = @_;
  my ($create) = grep { $_->[1] =~ m{/create$} } @{ $app->{liercd} };
  ok $create && index($create->[2], $bytes) >= 0 && $create->[2] !~ /[^\x00-\xff]/,
    "$what sends liercd UTF-8 bytes";
}

{
  my $app = FakeApp->new;
  API::Controller::Connection::create($app, FakeReq->new(undef, $body));
  stored_ok $app, "create";
  liercd_ok $app, "create";
}

{
  my $app = FakeApp->new($saved);
  my $res = API::Controller::Connection::show($app, FakeReq->new("c1"));
  is $res->{Config}{Highlight}[0], $cake, "show decodes the stored characters";
}

{
  my $app = FakeApp->new($saved);
  API::Controller::Connection::save($app, FakeReq->new("c1", encode_json { Highlight => [$cake, "lee"] }));
  stored_ok $app, "save";
}

{
  # a changed nick makes edit reconnect
  my $app = FakeApp->new($saved);
  API::Controller::Connection::edit($app, FakeReq->new("c1", encode_json { %base, Nick => "lee2", Highlight => [$cake] }));
  stored_ok $app, "edit";
  liercd_ok $app, "edit";
}

{
  my $app = FakeApp->new;
  API::Controller::Auth::add_default_network($app, "u1", "lee");
  stored_ok $app, "the default network";
  liercd_ok $app, "the default network";
}

done_testing;
