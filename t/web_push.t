use strict;
use warnings;

use Test::More;
use API::Routes;
use API::Request;
use API::Controller::WebPush;

# DELETE /notification/web_push?endpoint=<url> forgets a subscription. The
# endpoint used to go in the path, which nginx mangles ("https://" becomes
# "https:/"); that route is gone.

my $endpoint = "https://fcm.googleapis.com/fcm/send/abc:APA91b-x_y";

my ($name) = API::Routes->route({ REQUEST_METHOD => "DELETE", PATH_INFO => "/notification/web_push" });
is $name, "webpush.delete", "DELETE /notification/web_push routes to webpush.delete";
my ($old) = API::Routes->route({ REQUEST_METHOD => "DELETE", PATH_INFO => "/notification/web_push/https:/fcm.googleapis.com/fcm/send/abc" });
is $old, undef, "the old path form is gone";

{
  package FakeApp;
  sub new { bless { deleted => [] }, shift }
  sub dbh { bless { app => $_[0] }, "FakeDBH" }
  sub nocontent { [204, [], []] }
  sub error { [400, [], [$_[1]]] }
  package FakeDBH;
  sub prepare { bless { app => $_[0]{app} }, "FakeSTH" }
  package FakeSTH;
  sub execute { my ($self, @args) = @_; push @{ $self->{app}{deleted} }, [@args] }
  sub finish {}
}

sub delete_req {
  my ($query) = @_;
  my $app = FakeApp->new;
  my $env = { REQUEST_METHOD => "DELETE", QUERY_STRING => $query // "", PATH_INFO => "/" };
  my $req = API::Request->new($env, {}, { user => "u1" });
  my $res = API::Controller::WebPush::delete($app, $req);
  return ($res, $app->{deleted});
}

{
  my $q = "endpoint=" . join "", map { /[A-Za-z0-9_.~-]/ ? $_ : sprintf "%%%02X", ord } split //, $endpoint;
  my ($res, $deleted) = delete_req($q);
  is $res->[0], 204, "query form: 204";
  is_deeply $deleted, [["u1", $endpoint]], "deletes the user's row for the exact endpoint";
}

{
  my ($res, $deleted) = delete_req("");
  is $res->[0], 400, "no endpoint: 400";
  is_deeply $deleted, [], "and nothing deleted";
}

# Saving a subscription: only https endpoints on the browsers' push
# services, with a P-256 key and auth secret of the right size.
use MIME::Base64 qw(encode_base64url);
my $key  = encode_base64url("\x04" . ("k" x 64));
my $auth = encode_base64url("a" x 16);
my $check = \&API::Controller::WebPush::check_subscription;

for my $ok (
  "https://fcm.googleapis.com/fcm/send/abc:APA91b-x_y",
  "https://updates.push.services.mozilla.com/wpush/v2/gAAAAA",
  "https://wns2-par02p.notify.windows.com/w/?token=BQYAAA%3d",
  "https://web.push.apple.com/QGuQyavXutnMH",
  "https://FCM.googleapis.com:443/fcm/send/abc",
) {
  is $check->($ok, $key, $auth), undef, "accepts $ok";
}

for (
  ["http://fcm.googleapis.com/fcm/send/abc", "endpoint must be an https URL"],
  ["https://127.0.0.1:5005/abc/destroy",      "endpoint must be an https URL"],
  ["https://127.0.0.1/abc/destroy",           "endpoint is not a known push service"],
  ["https://localhost/push",                  "endpoint is not a known push service"],
  ["https://fcm.googleapis.com.evil.example/x", "endpoint is not a known push service"],
  ["https://evil.example/fcm.googleapis.com", "endpoint is not a known push service"],
  ["https://notify.windows.com/x",            "endpoint is not a known push service"],
  ["https://fcm.googleapis.com:4151/pub",     "endpoint must be an https URL"],
  ['https://user@fcm.googleapis.com/x',       "endpoint must be an https URL"],
  ["https://fcm.googleapis.com/a b",          "endpoint must be an https URL"],
  ["https://fcm.googleapis.com/" . ("x" x 250), "endpoint is too long"],
  ["",                                        "endpoint is required"],
) {
  my ($endpoint, $error) = @$_;
  is $check->($endpoint, $key, $auth), $error, "rejects $endpoint";
}

my $fcm = "https://fcm.googleapis.com/fcm/send/abc";
is $check->($fcm, undef, $auth), "key is required", "no key";
is $check->($fcm, "not base64!", $auth), "key is not base64url", "bad key encoding";
is $check->($fcm, encode_base64url("\x04" . ("k" x 10)), $auth), "key has the wrong length", "short key";
is $check->($fcm, encode_base64url("\x02" . ("k" x 64)), $auth), "key is not an uncompressed P-256 key", "compressed key";
is $check->($fcm, $key, encode_base64url("a" x 8)), "auth has the wrong length", "short auth";
is $check->($fcm, $key, ""), "auth is required", "no auth";

{
  package FakeUpsertReq;
  sub new { my ($c, %p) = @_; bless { p => \%p }, $c }
  sub parameters { $_[0]{p} }
  sub session { { user => "u1" } }
}

{
  my $app = FakeApp->new;
  my $res = API::Controller::WebPush::upsert($app, FakeUpsertReq->new(endpoint => "http://127.0.0.1:5005/x/destroy", key => $key, auth => $auth));
  is $res->[0], 400, "upsert refuses a bad endpoint";
  is_deeply $app->{deleted}, [], "without touching the database";

  $res = API::Controller::WebPush::upsert($app, FakeUpsertReq->new(endpoint => $fcm, key => $key, auth => $auth));
  is $res->[0], 204, "and saves a good one";
  is $app->{deleted}[0][0], $fcm, "with its endpoint";
}

done_testing;
