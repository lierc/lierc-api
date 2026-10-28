use strict;
use warnings;

use Test::More;
use Plack::Test;
use HTTP::Request::Common;
use API::SameOrigin;

my $app = API::SameOrigin->wrap(
  sub { [200, [], ["ok"]] },
  origins => ["https://relaychat.party", "http://localhost:8080"],
);

my @cases = (
  # method, headers, expected status, name
  [GET  => [], 200, "GET is always allowed"],
  [GET  => ["Sec-Fetch-Site" => "cross-site"], 200, "cross-site GET is allowed"],
  [POST => [], 200, "POST without browser headers is allowed"],
  [POST => ["Sec-Fetch-Site" => "same-origin"], 200, "same-origin POST"],
  [POST => ["Sec-Fetch-Site" => "none"], 200, "user-initiated POST"],
  [POST => ["Sec-Fetch-Site" => "cross-site"], 403, "cross-site POST"],
  [POST => ["Sec-Fetch-Site" => "same-site"], 403, "same-site (other subdomain) POST"],
  [DELETE => ["Sec-Fetch-Site" => "cross-site"], 403, "cross-site DELETE"],
  [POST => ["Sec-Fetch-Site" => "cross-site", Origin => "https://relaychat.party"],
    403, "Sec-Fetch-Site wins over Origin"],
  [POST => [Origin => "https://relaychat.party"], 200, "allowed Origin"],
  [POST => [Origin => "http://localhost:8080"], 200, "other allowed Origin"],
  [POST => [Origin => "https://evil.example"], 403, "other Origin"],
  [POST => [Origin => "null"], 403, "null Origin"],
);

test_psgi $app, sub {
  my $cb = shift;
  for (@cases) {
    my ($method, $headers, $status, $name) = @$_;
    my $req = HTTP::Request->new($method => "/connection/abc", $headers);
    is $cb->($req)->code, $status, $name;
  }
};

my $none = API::SameOrigin->wrap(sub { [200, [], ["ok"]] });
test_psgi $none, sub {
  my $cb = shift;
  is $cb->(POST "/x", Origin => "https://relaychat.party")->code, 403,
    "no origins configured refuses any Origin";
};

done_testing;
