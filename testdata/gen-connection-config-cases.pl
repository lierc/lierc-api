#!/usr/bin/env perl
# Writes testdata/connection-config-cases.json from what Perl's
# API::ConnectionConfig actually returns. Go (internal/connconfig) must match
# every case whose fields have their proper JSON types, and refuse the rest. Add inputs here, then regenerate in the API image:
#
#   docker build -q -t lierc-api-test . >/dev/null
#   docker run --rm lierc-api-test perl -Ilocal/lib/perl5 -Ilib \
#     testdata/gen-connection-config-cases.pl > testdata/connection-config-cases.json
#
# and run both test suites.
use strict;
use warnings;
use utf8;
use JSON::XS;
use API::ConnectionConfig;

my $C = "API::ConnectionConfig";
my $J = JSON::XS->new->canonical;
my $base = q{"Host":"irc.libera.chat","Port":6697,"Nick":"lee"};
sub with { my $extra = shift; "{$base,$extra}" }   # extra fields override (later key wins)

my $label = "a" x 63;
my $long_ok = join(".", ($label) x 3, "a" x 61);
my $long254 = join(".", ($label) x 3, "a" x 62);

my @clean = (
  ["full config, normalised", q({"Host":" ergo ","Port":"6667","Nick":"lee","Channels":["#a",""," #b "],"Highlight":["lee",""],"Alias":" my net ","Ssl":1,"Pass":" pw ","Extra":1})],
  ["blank optionals omitted", with(q{"User":"","Alias":"  ","Pass":""})],
  (map { ["Host $_", with(qq{"Host":"$_"})] } "irc.libera.chat", "1.2.3.4", "a-b.c", "ergo", "Irc.Example.COM",
     "::1", "-a.com", "a-.com", "a..b", "a.com.", "a_b.com", "a b", "1.2.3.256", "a" x 64, "", " "),
  ["Host with a newline inside a label", with(q{"Host":"a\n.com"})],
  ["Host with a newline at the end of a label", with(q{"Host":"irc\n.libera.chat"})],
  ["Host of 253 characters", with(qq{"Host":"$long_ok"})],
  ["Host of 254 characters", with(qq{"Host":"$long254"})],
  ["Host of 255 characters", with(qq{"Host":"$long_ok.a"})],
  ["missing Host", q({"Port":6667,"Nick":"lee"})],
  ["Host null", with(q{"Host":null})],
  ["Host list", with(q{"Host":["x"]})],
  ["Host number", with(q{"Host":123})],
  ["Host true", with(q{"Host":true})],
  ["Host NBSP-padded", with(qq{"Host":"\x{a0}ergo\x{a0}"})],
  (map { ["Port $_", with(qq{"Port":$_})] } "1", "65535", '"6697"', '" 6697 "', "0", "65536", '"abc"', '"66 97"',
     "6.5", '""', "-1", "6667.0", "1e3", "true", "false", "null", "[]", "{}", '"06667"'),
  ["missing Port", q({"Host":"a.com","Nick":"lee"})],
  (map { ["Nick $_", with(qq{"Nick":$_})] } '"lee"', '"[away]"', '"_2cool"', '"a-b"', '"' . ("a" x 32) . '"',
     '"`x\\\\y^{|}"', '"2cool"', '"-a"', '"a b"', '"a.b"', '"' . ("a" x 33) . '"', '"lée"', '""', '"  "',
     "12", "1.5", "true", "null", '[]', '" lee "', qq{"\x{a0}lee"}, qq{"\x{3000}lee\x{3000}"}, '"lee\n"', '"le\te"'),
  (map { ["Nick $_", with(qq{"Nick":$_})] } qq{"l\x{e9}e"}, qq{"\x{540d}\x{524d}"}, qq{"ann\x{1f9d9}"}, '"a,b"', '"a*"', '"a?"',
     '"a!b"', '"a@b"', '"#a"', '"&a"', '"$a"', '":a"', '"a#b"', qq{"a\x{3000}b"}, '"a\u0085b"', '"a\u009fb"', '"a\u007fb"',
     '"' . ("\x{e9}" x 32) . '"', '"' . ("\x{e9}" x 33) . '"'),
  ["missing Nick", q({"Host":"a.com","Port":1})],
  (map { ["User $_", with(qq{"User":$_})] } '" ~lee.a "', '"2cool"', '"lee/libera"', '"a b"', '"a:b"', '":a"', '"a\\tb"', '"a\\u0000b"', '"a\\u0007b"', qq{"l\x{e9}e"}, qq{"a\x{a0}b"}, '"a@b"', '"' . ("a" x 33) . '"',
     "12", "true", "null", '[]', '""'),
  (map { ["Pass $_", with(qq{"Pass":$_})] } '"a\r\nQUIT"', '"a\nb"', '"a\rb"', '"a\u0000b"', '"a b"', '" pw "',
     '["a"]', "true", "false", "12", "1.5", "0", "null", '""', '{}'),
  (map { ["Channels $_", with(qq{"Channels":$_})] } '["#Foo-Bar"]', '["&local"]', '["##java"]', '["+x"]',
     '["!12345x"]', qq{["#\x{e9}"]}, '["#' . ("a" x 199) . '"]', '["a"]', '["#a b"]', '["#a,#b"]', '["#a\u0007"]',
     '["#' . ("a" x 200) . '"]', '["#"]', '"#a"', '[["#a"]]', '["#a", 1]', '["#a", null]', '["#a", true]',
     qq{["#a\x{a0}b"]}, qq{["#\x{3000}"]}, '["#a\u0085b"]', '[]', 'null', '{}', '["", " "]'),
  (map { ["Highlight $_", with(qq{"Highlight":$_})] } '"lee"', '["a\nb"]', '["lee", ""]', '[1]', '[null]',
     qq{["\x{a0}"]}, '["a\u007fb"]'),
  (map { ["Alias $_", with(qq{"Alias":$_})] } '"Libera Chat 2"', '"libera.chat"', '"my.net 2"', '"."', '"a-b"', '"irc-net.org 2"', '"a_b"', '"a/b"', '"' . ("a" x 33) . '"',
     '"  "', "12", "true", "null", qq{"\x{a0}net"}),
  (map { ["Ssl $_", with(qq{"Ssl":$_,"SASL":$_})] } "true", "false", "1", "0", "0.0", '"0"', '"false"', '""',
     '"0.0"', '"00"', '[]', '{}', "null", '" "'),
  (map { ["big number $_->[0] $_->[1]", with(qq{"$_->[0]":$_->[1]})] }
     map { my $f = $_; map { [$f, $_] } "12345678901234567890123", "-12345678901234567890", "18446744073709551616",
       "100000000000000000000", "9223372036854775808", "1E400", "-1E400" } qw(User Alias Nick Host Pass)),
  (map { ["big number $_ 1e-400", with(qq{"$_":1e-400})] } qw(Nick Host Pass)),
  (map { ["big number Highlight $_", with(qq{"Highlight":[$_]})] } "12345678901234567890123", "1E400"),
  (map { ["big number Channels $_", with(qq{"Channels":[$_]})] } "12345678901234567890123"),
  (map { ["big number Port $_", with(qq{"Port":$_})] } "12345678901234567890123", "1E400"),
  (map { ["big number Ssl $_", with(qq{"Ssl":$_})] } "12345678901234567890123", "1E400", "-1E400"),
  ["unknown keys dropped", with(q{"Foo":1,"Id":"x"})],
  ["input list", q([])],
  ["input string", q("x")],
  ["input null", q(null)],
  ["input empty object", q({})],
);

sub result {
  my $config = eval { $C->clean(shift) };
  return { want => $config } if $config;
  (my $e = $@) =~ s/\n\z//;
  return { error => $e };
}

my @clean_out = map {
  my ($name, $json) = @$_;
  my $params = eval { JSON::XS->new->allow_nonref->decode($json) };
  die "bad input JSON for $name: $@" if $@;
  +{ name => $name, input => $json, %{ result($params) } };
} @clean;

my @blocked_out = map { +{ ip => $_, blocked => $C->blocked_ip($_) ? JSON::XS::true : JSON::XS::false } } qw(
  127.0.0.1 10.0.0.1 100.64.0.1 100.127.255.255 169.254.1.1 172.16.0.1
  172.31.255.255 192.168.1.1 192.0.0.1 198.18.0.1 198.19.255.255 0.0.0.0
  224.0.0.1 255.255.255.255 :: ::1 fd00::1 fe80::1 ff02::1 64:ff9b::1
  ::ffff:127.0.0.1 ::ffff:10.1.2.3
  8.8.8.8 100.63.255.255 100.128.0.0 172.15.255.255 172.32.0.0
  198.17.255.255 198.20.0.0 192.0.1.1 2001:4860::8888 ::ffff:8.8.8.8
  fc00::1 fbff::1 fe7f::1 febf::1 fec0::1 64:ff9b::1:1 not-an-ip 1.2.3 ::ffff:0:0
);

my @exempt_in = (
  ["irc.relaychat.party", "", "irc.relaychat.party", 6667],
  ["irc.relaychat.party", "", "IRC.relaychat.party", 6667],
  ["IRC.relaychat.party", "", "irc.relaychat.party", 6667],
  ["irc.relaychat.party", "", "irc.relaychat.party", 5005],
  ["irc.relaychat.party", "6697", "irc.relaychat.party", 6697],
  ["irc.relaychat.party", "6697", "irc.relaychat.party", 6667],
  ["irc.relaychat.party", "abc", "irc.relaychat.party", 6667],
  ["irc.relaychat.party", "0", "irc.relaychat.party", 6667],
  ["irc.relaychat.party", " 6697", "irc.relaychat.party", 6697],
  ["irc.relaychat.party", "6697x", "irc.relaychat.party", 6697],
  ["", "", "irc.relaychat.party", 6667],
  [undef, undef, "irc.relaychat.party", 6667],
  ["ergo", "", "ergo.", 6667],
);
my @exempt_out = map {
  my ($eh, $ep, $h, $p) = @$_;
  local $ENV{DEFAULT_NETWORK_HOST} = $eh;
  local $ENV{DEFAULT_NETWORK_PORT} = $ep;
  delete $ENV{DEFAULT_NETWORK_HOST} unless defined $eh;
  delete $ENV{DEFAULT_NETWORK_PORT} unless defined $ep;
  +{ env_host => $eh, env_port => $ep, host => $h, port => $p,
     exempt => $C->exempt($h, $p) ? JSON::XS::true : JSON::XS::false }
} @exempt_in;

my @check_in = (
  [undef, undef, "a.com", 6667, ["8.8.8.8"]],
  [undef, undef, "a.com", 6667, ["8.8.8.8", "127.0.0.1"]],
  [undef, undef, "a.com", 6667, ["::ffff:127.0.0.1"]],
  [undef, undef, "a.com", 6667, ["2001:4860::8888"]],
  [undef, undef, "nosuch.invalid", 6667, []],
  [undef, undef, "127.0.0.1", 6667, ["8.8.8.8"]],
  [undef, undef, "8.8.8.8", 6667, ["127.0.0.1"]],
  [undef, undef, "ergo", 6667, ["172.18.0.5"]],
  ["IRC.relaychat.party", undef, "irc.relaychat.party", 6667, ["127.0.0.1"]],
  ["irc.relaychat.party", undef, "irc.relaychat.party", 5005, ["127.0.0.1"]],
  ["ergo", "6697", "ergo", 6697, ["172.18.0.5"]],
  ["ergo", "6697", "ergo", 6667, ["172.18.0.5"]],
  ["127.0.0.1", undef, "127.0.0.1", 6667, []],
);
my @check_out = map {
  my ($eh, $ep, $h, $p, $ips) = @$_;
  local $ENV{DEFAULT_NETWORK_HOST} = $eh;
  local $ENV{DEFAULT_NETWORK_PORT} = $ep;
  delete $ENV{DEFAULT_NETWORK_HOST} unless defined $eh;
  delete $ENV{DEFAULT_NETWORK_PORT} unless defined $ep;
  my $called = 0;
  my $ok = eval { $C->check_host($h, $p, resolve => sub { $called++; @$ips }) };
  (my $e = $ok ? "" : $@) =~ s/\n\z//;
  +{ env_host => $eh, env_port => $ep, host => $h, port => $p, resolves => $ips,
     error => $e, resolved => $called ? JSON::XS::true : JSON::XS::false }
} @check_in;

binmode STDOUT;
print JSON::XS->new->utf8->canonical->pretty->encode({
  _comment => "Shared by t/connection_config.t and internal/connconfig. Expected values are what lib/API/ConnectionConfig.pm returns; 'input' is the raw request JSON. Go must match every case whose fields have their proper JSON types (strings, a number for Port, booleans for Ssl/SASL) and refuse the rest; it doesn't follow Perl's type conversions. Keep Perl and Go in step: a rule changed in one must pass here in both.",
  clean => \@clean_out, blocked => \@blocked_out, exempt => \@exempt_out, check_host => \@check_out,
});
