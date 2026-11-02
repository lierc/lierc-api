use strict;
use warnings;

use Test::More;
use JSON::XS;
use FindBin;
use API::ConnectionConfig;

# The rule cases are shared with the Go copy (internal/connconfig): both
# test suites run every case in testdata/connection-config-cases.json.

my $C = "API::ConnectionConfig";
my $J = JSON::XS->new->canonical;

my $file = "$FindBin::Bin/../testdata/connection-config-cases.json";
open my $fh, "<:raw", $file or die "can't open $file: $!";
my $cases = decode_json(do { local $/; <$fh> });

sub stripped {
  (my $err = shift) =~ s/ at \S+ line \d+.*//s;
  $err =~ s/\n\z//;
  return $err;
}

sub with_env {
  my ($case, $code) = @_;
  local $ENV{DEFAULT_NETWORK_HOST} = $case->{env_host};
  local $ENV{DEFAULT_NETWORK_PORT} = $case->{env_port};
  delete $ENV{DEFAULT_NETWORK_HOST} unless defined $case->{env_host};
  delete $ENV{DEFAULT_NETWORK_PORT} unless defined $case->{env_port};
  return $code->();
}

for my $case (@{ $cases->{clean} }) {
  # the input is the raw request body, decoded as the API decodes it
  my $params = JSON::XS->new->allow_nonref->decode($case->{input});
  my $config = eval { $C->clean($params) };
  my $err = $@;
  if (exists $case->{want}) {
    is($config && $J->encode($config), $J->encode($case->{want}), "clean: $case->{name}")
      or diag "died: $err";
  }
  else {
    ok(!$config, "clean: $case->{name} rejected");
    is(stripped($err), $case->{error}, "clean: $case->{name} message");
  }
}

for my $case (@{ $cases->{blocked} }) {
  is(!!$C->blocked_ip($case->{ip}), !!$case->{blocked}, "blocked_ip $case->{ip}");
}

for my $case (@{ $cases->{exempt} }) {
  my $got = with_env($case, sub { no warnings "numeric"; $C->exempt($case->{host}, $case->{port}) });
  is(!!$got, !!$case->{exempt},
    sprintf "exempt %s:%s with %s:%s", $case->{host}, $case->{port},
      $case->{env_host} // "unset", $case->{env_port} // "unset");
}

for my $case (@{ $cases->{check_host} }) {
  my $called = 0;
  my $ok = with_env($case, sub {
    no warnings "numeric";
    eval { $C->check_host($case->{host}, $case->{port}, resolve => sub { $called++; @{ $case->{resolves} } }) };
  });
  my $err = $ok ? "" : stripped($@);
  my $name = "check_host $case->{host}:$case->{port} -> @{ $case->{resolves} }";
  is($err, $case->{error}, $name);
  is(!!$called, !!$case->{resolved}, "$name: resolver " . ($case->{resolved} ? "called" : "not called"));
}

# Perl only: the real resolver
{
  local $ENV{DEFAULT_NETWORK_HOST};
  my $ok = eval { $C->check_host("localhost", 6667) };
  like($@, qr/^Host localhost points to a private address/, "real resolver: localhost");
}

done_testing;
