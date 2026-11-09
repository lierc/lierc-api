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

done_testing;
