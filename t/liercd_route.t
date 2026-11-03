use strict;
use warnings;
use Test::More;
use HTTP::Response;

{
  package Stub::UA;
  sub new { bless { urls => [] }, shift }
  sub request {
    my ($self, $req) = @_;
    push @{ $self->{urls} }, $req->uri . "";
    my $res = HTTP::Response->new(200);
    $res->content("{}");
    return $res;
  }
}

{
  package Stub::App;
  use Role::Tiny::With;
  with 'API::Liercd';
  our (%INST, $NEXT);
  sub new { bless { ua => Stub::UA->new }, shift }
  sub host { "main.example" }
  sub next_host { $NEXT }
  sub lookup_liercd { $INST{$_[1]} }
  sub lookup_config { '{"x":1}' }
}

my $app = Stub::App->new;
sub urls { my @u = @{ $app->{ua}{urls} }; $app->{ua}{urls} = []; @u }

$Stub::App::NEXT = "next.example";
%Stub::App::INST = (c1 => undef, n1 => "next", n2 => "next", b1 => "bogus", e1 => "next");

$app->request(GET => "c1/status");
is_deeply [urls()], ["http://main.example:5005/c1/status"], "NULL routes to main";

$app->request(GET => "n1/status");
is_deeply [urls()], ["http://next.example:5005/n1/status"], "next routes to next_host";

$app->request(POST => "/n2/raw", "x");
is_deeply [urls()], ["http://next.example:5005//n2/raw"], "leading slash id found";

$app->request(GET => "unknown/status");
is_deeply [urls()], ["http://main.example:5005/unknown/status"], "no row routes to main";

eval { $app->request(GET => "b1/status") };
like $@, qr/unknown liercd instance "bogus"/, "unknown instance dies";
is_deeply [urls()], [], "no request made";

for my $v (undef, "") {
  $Stub::App::NEXT = $v;
  eval { $app->request(GET => "n1/status") };
  like $@, qr/liercd instance "next" has no host \(LIERCD_NEXT_HOST\)/, "next without host dies";
  is_deeply [urls()], [], "no request made";
}

# find_or_recreate_connection: status fails, create, status ok
$Stub::App::NEXT = "next.example";
{
  no warnings 'redefine';
  my $n = 0;
  local *Stub::UA::request = sub {
    my ($self, $req) = @_;
    push @{ $self->{urls} }, $req->uri . "";
    my $res = HTTP::Response->new(++$n == 1 ? 404 : 200);
    return $res;
  };
  $app->find_or_recreate_connection("n1");
  is_deeply [urls()], [map "http://next.example:5005/n1/$_", qw(status create status)],
    "recreate talks to next throughout";
}

done_testing;
