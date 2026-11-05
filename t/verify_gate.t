use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::Routes;
use API::SignedToken;
use API::Controller::Auth;

# A signup that hasn't confirmed its email (verified false) can't use the
# app: every route but a few auth ones answers 403 with unverified set. An
# account from before that rule (verified NULL) and a verified one are let
# in. Signing up no longer adds the default network; the account's first
# verification does.

$ENV{LIERC_NO_SMTP} = 1;

my ($name) = API::Routes->route({ REQUEST_METHOD => "POST", PATH_INFO => "/verify/resend" });
is $name, "auth.resend_verify", "POST /verify/resend routes to auth.resend_verify";

{
  package FakeReq;
  sub new { my ($class, %p) = @_; bless { session => { user => "u1" }, params => {}, %p }, $class }
  sub captures { {} }
  sub session { $_[0]{session} }
  sub parameters { $_[0]{params} }

  package FakeRes;
  sub new { bless { code => $_[1] }, $_[0] }
  sub code { $_[0]{code} }
  sub status_line { "$_[0]{code}" }

  # the dispatcher with the real responses, and run standing in for the
  # route handlers
  package GateApp;
  use Role::Tiny::With;
  with 'API::Dispatch';
  with 'API::Responses';
  sub new { my ($class, $user) = @_; bless { user => $user }, $class }
  sub logged_in { $_[0]{user} }
  sub verify_owner { 1 }
  sub run { "ran $_[1]" }

  # what the auth handlers use
  package AuthApp;
  sub new {
    my ($class, %p) = @_;
    bless { saves => [], liercd => [], sql => [], first => 1, user => {}, %p }, $class;
  }
  sub secret { "s3cret" }
  sub site_url { "https://example.com" }
  sub from_address { 'noreply@example.com' }
  sub default_network { { Host => "irc.example.com", Port => 6697, Ssl => JSON::XS::true } }
  sub lookup_user { $_[0]{user} }
  sub add_user { "u1" }
  sub save_connection { my ($self, @args) = @_; push @{ $self->{saves} }, [@args] }
  sub request { my ($self, @args) = @_; push @{ $self->{liercd} }, [@args]; FakeRes->new(200) }
  sub handle { "shown" }
  sub json { $_[1] }
  sub ok { "ok" }
  sub error { "error: $_[1]" }
  sub dbh { my $self = shift; bless { app => $self }, "FakeDBH" }

  package FakeDBH;
  sub selectrow_hashref { $_[0]{app}{user} }
  # whether the user already has a connection saved
  sub selectrow_array { $_[1] =~ /FROM connection/ && $_[0]{app}{has_connection} ? (1) : () }
  sub do {
    my ($self, $sql) = @_;
    push @{ $self->{app}{sql} }, $sql;
    # the first verification's update changes a row, a later one doesn't
    return $self->{app}{first} ? 1 : "0E0" if $sql =~ /verified\s*=\s*false/;
    return 1;
  }
}

# the gate
for my $case ([0, "unverified"], [undef, "grandfathered"], [1, "verified"]) {
  my ($verified, $what) = @$case;
  my $app = GateApp->new({ id => "u1", verified => $verified });
  my $res = $app->handle("connection.list", FakeReq->new);
  if (defined $verified && !$verified) {
    is ref $res eq "ARRAY" ? $res->[0] : $res, 403, "$what: a gated route is 403";
    is_deeply ref $res eq "ARRAY" ? decode_json($res->[2][0]) : $res,
      { status => "error", error => "Confirm your email address first", unverified => JSON::XS::true },
      "$what: with unverified set";
    is $app->handle($_, FakeReq->new), "ran $_", "$what: $_ is allowed"
      for qw(auth.show auth.logout auth.resend_verify);
  }
  else {
    is $res, "ran connection.list", "$what: let in";
  }
}
is +(GateApp->new(undef)->handle("connection.list", FakeReq->new))->[0], 401, "logged out is still 401";

# /auth says whether the account is verified
for my $case ([0, JSON::XS::false], [1, JSON::XS::true], [undef, undef]) {
  my ($verified, $want) = @$case;
  my $app = AuthApp->new(user => { id => "u1", username => "ann", email => 'ann@example.com', verified => $verified });
  my $shown = API::Controller::Auth::show($app, FakeReq->new);
  is_deeply $shown, { id => "u1", user => "ann", email => 'ann@example.com', verified => $want },
    "auth.show verified for " . ($verified // "NULL");
}

# signing up adds no network
{
  my $app = AuthApp->new;
  my $req = FakeReq->new(session => {}, params => { username => "ann", email => 'ann@example.com', pass => "hunter22" });
  my $res = API::Controller::Auth::register($app, $req);
  $res->(sub {}) if ref $res eq "CODE";
  is_deeply $app->{saves}, [], "register saves no connection";
  is_deeply $app->{liercd}, [], "and starts none";
  is $req->session->{user}, "u1", "and logs the new user in";
}

# verifying adds the default network, once
sub verify_token {
  API::SignedToken->make(purpose => "verify", secret => "s3cret", user => "u1",
    fingerprint => 'ann@example.com', expires => time + 3600);
}
for my $first (1, 0) {
  my $app = AuthApp->new(first => $first, user => { id => "u1", username => "ann", email => 'ann@example.com' });
  my $res = API::Controller::Auth::verify($app, FakeReq->new(params => { token => verify_token() }));
  is $res, "ok", "verify answers ok";
  if ($first) {
    is scalar @{ $app->{saves} }, 1, "the first verification saves the default network";
    is $app->{saves}[0][1], "u1", "for the user";
    is decode_json($app->{saves}[0][2])->{Nick}, "ann", "with their nick";
    like $app->{liercd}[0][1], qr{/create$}, "and starts it";
  }
  else {
    is_deeply $app->{saves}, [], "a later verification adds no network";
  }
}

# an account from before this change, which got the default network at
# signup, gets no second one when it first verifies
{
  my $app = AuthApp->new(first => 1, has_connection => 1, user => { id => "u1", username => "ann", email => 'ann@example.com' });
  API::Controller::Auth::verify($app, FakeReq->new(params => { token => verify_token() }));
  is_deeply $app->{saves}, [], "a first verification with a connection already saved adds none";
}

# resend: one email a minute per session (every API worker sees the session
# cookie), only while unverified
{
  my @sent;
  no warnings 'redefine';
  local *API::Controller::Auth::send_verify_email = sub { push @sent, $_[1] };
  my $resend = sub {
    my ($user, $session) = @_;
    my $app = AuthApp->new(user => $user);
    my $res = API::Controller::Auth::resend_verify($app, FakeReq->new(session => $session));
    my $got;
    if (ref $res eq "CODE") { $res->(sub { $got = shift }) } else { $got = $res }
    return $got;
  };
  my $ann = { id => "u1", username => "ann", email => 'ann@example.com', verified => 0 };
  my $session = { user => "u1" };
  is $resend->($ann, $session), "ok", "resend answers ok";
  is_deeply \@sent, ["u1"], "and sends";
  ok $session->{verify_resent}, "and notes the time in the session";
  is $resend->($ann, $session), "ok", "a second resend within a minute answers ok";
  is_deeply \@sent, ["u1"], "but doesn't send";
  $session->{verify_resent} = time - 61;
  $resend->($ann, $session);
  is_deeply \@sent, ["u1", "u1"], "a minute later it sends again";
  $resend->({ id => "u3", username => "cy", email => 'cy@example.com', verified => 1 }, { user => "u3" });
  $resend->({ id => "u4", username => "di", email => 'di@example.com', verified => undef }, { user => "u4" });
  is_deeply \@sent, ["u1", "u1"], "verified and grandfathered accounts get no email";
}

done_testing;
