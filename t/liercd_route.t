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
%Stub::App::INST = (c1 => undef, n1 => "next", n2 => "next", b1 => "bogus", e1 => "next", z1 => "");

$app->request(GET => "c1/status");
is_deeply [urls()], ["http://main.example:5005/c1/status"], "NULL routes to main";

$app->request(GET => "n1/status");
is_deeply [urls()], ["http://next.example:5005/n1/status"], "next routes to next_host";

$app->request(POST => "/n2/raw", "x");
is_deeply [urls()], ["http://next.example:5005//n2/raw"], "leading slash id found";

$app->request(GET => "z1/status");
is_deeply [urls()], ["http://main.example:5005/z1/status"], "'' routes to main, as Go does";

$app->request(GET => "unknown/status");
is_deeply [urls()], ["http://main.example:5005/unknown/status"], "no row routes to main";

eval { $app->request(GET => "b1/status") };
is $@, qq{unknown liercd instance "bogus"\n}, "unknown instance dies, without a file and line";
is_deeply [urls()], [], "no request made";

# a host may give its own port; without one it's liercd's 5005
$Stub::App::NEXT = "127.0.0.1:5006";
$app->request(GET => "n1/status");
is_deeply [urls()], ["http://127.0.0.1:5006/n1/status"], "a next host with a port keeps it";

for my $v (undef, "") {
  $Stub::App::NEXT = $v;
  eval { $app->request(GET => "n1/status") };
  is $@, qq{liercd instance "next" has no host (LIERCD_NEXT_HOST)\n}, "next without host dies, without a file and line";
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

# The real API class, built the way bin/api.psgi builds it.
{
  require API;
  require API::Config;
  {
    package API::Test;
    our @ISA = ('API');
    our %INST;
    sub lookup_liercd { $INST{$_[1]} }
  }
  local %API::Test::INST = (n1 => "next");

  local $ENV{LIERCD_HOST} = "main.example";
  local $ENV{LIERCD_NEXT_HOST} = "next.example";
  my $real = API::Test->new(API::Config->new->as_hash);
  $real->{ua} = Stub::UA->new;
  $real->request(GET => "n1/status");
  $real->request(GET => "c1/status");
  is_deeply $real->{ua}{urls},
    ["http://next.example:5005/n1/status", "http://main.example:5005/c1/status"],
    "real API routes via as_hash config";

  delete $ENV{LIERCD_NEXT_HOST};
  my $unset = API::Test->new(API::Config->new->as_hash);
  $unset->{ua} = Stub::UA->new;
  eval { $unset->request(GET => "n1/status") };
  like $@, qr/liercd instance "next" has no host \(LIERCD_NEXT_HOST\)/, "real API: unset next_host dies";
  is_deeply $unset->{ua}{urls}, [], "no request made";
}

done_testing;
