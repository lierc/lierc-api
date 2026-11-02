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

# The nick rule, written out (the shared cases above come from running it)
{
  my $nick_err = "Nick must be 1 to 32 characters, without spaces or , * ? ! @, and can't start with # & \$ or :";
  my $ok = sub { my $n = shift; my $c = eval { $C->clean({ Host => "a.com", Port => 6667, Nick => $n }) }; $c && $c->{Nick} };
  my $err = sub { my $n = shift; eval { $C->clean({ Host => "a.com", Port => 6667, Nick => $n }) }; (my $e = $@) =~ s/\n\z//; $e };
  is($ok->($_), $_, "nick $_ accepted") for "l\x{e9}e", "\x{540d}\x{524d}", "ann\x{1f9d9}", "2cool", "-dash", "a.b", "[away]", "a" x 32, "\x{e9}" x 32;
  is($err->($_), $nick_err, "nick refused") for "a b", "a,b", "a*", "a?", "a!b", "a\@b", "#a", "&a", "\$a", ":a",
    "a\x{a0}b", "a\x{3000}b", "a\x07b", "a\x7fb", "a\x{85}b", "a\x{9f}b", "a" x 33, "\x{e9}" x 33;
  is($ok->("a#b"), "a#b", "# allowed after the first character");
}

# The alias rule, written out
{
  my $alias_err = "Network alias can only use letters, digits, spaces, dots and hyphens, up to 32 characters";
  my $run = sub { my $a = shift; my $c = eval { $C->clean({ Host => "a.com", Port => 6667, Nick => "lee", Alias => $a }) }; (my $e = $@) =~ s/\n\z//; ($c && $c->{Alias}, $e) };
  is(($run->($_))[0], $_, "alias $_ accepted") for "libera.chat", "Libera Chat 2", "my.net 2", ".", "a-b", "-", "irc-net.org 2", "a" x 32;
  is(($run->($_))[1], $alias_err, "alias $_ refused") for "a_b", "a,b", "a/b", "l\x{e9}e", "a\tb", "a" x 33;
}

# The username rule, written out
{
  my $user_err = "Username must be up to 32 characters, without spaces or \@, and can't start with :";
  my $run = sub { my $u = shift; my $c = eval { $C->clean({ Host => "a.com", Port => 6667, Nick => "lee", User => $u }) }; (my $e = $@) =~ s/\n\z//; ($c && $c->{User}, $e) };
  is(($run->($_))[0], $_, "username $_ accepted") for "lee/libera", "lee/libera/2", "/", "~lee.a", "a:b", "a!b", "a#b", "l\x{e9}e", "\x{540d}\x{524d}", "a\tb", "a\x07b", "a\x7fb", "a\x{85}b", "a\x{a0}b", "a\x{3000}b", "a" x 32, "\x{e9}" x 32;
  is(($run->($_))[1], $user_err, "username $_ refused") for "a b", "a\@b", ":a", "a\x00b", "a\rb", "a\nb", "a" x 33, "\x{e9}" x 33;
}

# Perl only: the real resolver
{
  local $ENV{DEFAULT_NETWORK_HOST};
  my $ok = eval { $C->check_host("localhost", 6667) };
  like($@, qr/^Host localhost points to a private address/, "real resolver: localhost");
}

done_testing;
