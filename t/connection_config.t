use strict;
use warnings;

use Test::More;
use JSON::XS;
use API::ConnectionConfig;

my $C = "API::ConnectionConfig";

sub base { (Host => "irc.libera.chat", Port => 6697, Nick => "lee", @_) }

sub ok_clean {
  my ($params, $name) = @_;
  my $config = eval { $C->clean($params) };
  ok($config, $name) or diag "died: $@";
  return $config;
}

sub fails {
  my ($params, $message, $name) = @_;
  my $config = eval { $C->clean($params) };
  my $err = $@;
  $err =~ s/ at \S+ line \d+.*//s;
  $err =~ s/\n\z//;
  ok(!$config, "$name rejected");
  is($err, $message, "$name message");
}

# normalising
{
  my $config = ok_clean({
    Host => " ergo ", Port => "6667", Nick => "lee",
    Channels => ["#a", "", " #b "], Highlight => ["lee", ""],
    Alias => " my net ", Ssl => 1, Pass => " pw ", Extra => 1,
  }, "a full config");
  is_deeply($config, {
    Host => "ergo", Port => 6667, Nick => "lee",
    Channels => ["#a", "#b"], Highlight => ["lee"],
    Alias => "my net", Ssl => JSON::XS::true, SASL => JSON::XS::false,
    Pass => " pw ",
  }, "cleaned: trimmed, blanks dropped, unknown keys dropped, Pass kept as is");
  like(JSON::XS->new->canonical->encode($config), qr/"Port":6667[,}]/, "Port is a number");
  ok(!exists $config->{User}, "blank User omitted");
}
{
  my $config = ok_clean({ base(User => "", Alias => "  ", Pass => "") }, "blank optionals");
  ok(!exists $config->{$_}, "blank $_ omitted") for qw(User Alias Pass);
  is_deeply($config->{Channels}, [], "missing Channels is an empty list");
  is_deeply($config->{Highlight}, [], "missing Highlight is an empty list");
}

# Host
my $host_err = "Host must be an IPv4 address or a domain name";
my $label = "a" x 63;
my $long_ok = join(".", ($label) x 3, "a" x 61);   # 253
is(length $long_ok, 253, "253-character name");
ok_clean({ base(Host => $_) }, "Host $_") for "irc.libera.chat", "1.2.3.4", "a-b.c", "ergo", "Irc.Example.COM";
ok_clean({ base(Host => $long_ok) }, "Host of 253 characters");
fails({ base(Host => "") }, "Host is required", "empty Host");
fails({ Port => 6667, Nick => "lee" }, "Host is required", "missing Host");
fails({ base(Host => $_) }, $host_err, "Host '$_'")
  for "::1", "-a.com", "a-.com", "a..b", "a.com.", "a_b.com", "a b", "1.2.3.256", "a" x 64;
fails({ base(Host => "$long_ok.a") }, $host_err, "Host of 255 characters");
fails({ base(Host => join(".", ($label) x 3, "a" x 62)) }, $host_err, "Host of 254 characters");
fails({ base(Host => ["x"]) }, $host_err, "Host as a list");

# Port
my $port_err = "Port must be a number from 1 to 65535";
ok_clean({ base(Port => $_) }, "Port $_") for 1, 65535, "6697";
fails({ base(Port => $_) }, $port_err, "Port '$_'") for 0, 65536, "abc", "66 97", 6.5, "", -1;
fails({ Host => "a.com", Nick => "lee" }, $port_err, "missing Port");
fails({ base(Port => JSON::XS::true) }, $port_err, "Port true");

# Nick
my $nick_err = 'Nick must be up to 32 letters, digits, - or [ ] \ ` _ ^ { | }, not starting with a digit or -';
ok_clean({ base(Nick => $_) }, "Nick $_") for "lee", "[away]", "_2cool", "a-b", "a" x 32, '`x\\y^{|}';
fails({ base(Nick => $_) }, $nick_err, "Nick '$_'") for "2cool", "-a", "a b", "a.b", "a" x 33, "lée";
fails({ base(Nick => $_) }, "Nick is required", "Nick '$_'") for "", "  ";
fails({ Host => "a.com", Port => 1 }, "Nick is required", "missing Nick");

# User
my $user_err = 'Username must be up to 32 letters, digits, - . ~ or [ ] \ ` _ ^ { | }';
is(ok_clean({ base(User => " ~lee.a ") }, "User ~lee.a")->{User}, "~lee.a", "User trimmed");
ok_clean({ base(User => "2cool") }, "User may start with a digit");
fails({ base(User => $_) }, $user_err, "User '$_'") for "a b", 'a@b', "a" x 33;

# Pass
my $pass_err = "Password can't contain line breaks";
fails({ base(Pass => $_) }, $pass_err, "Pass with control") for "a\r\nQUIT", "a\nb", "a\rb", "a\0b";
is(ok_clean({ base(Pass => "a b") }, "Pass with a space")->{Pass}, "a b", "Pass kept");
fails({ base(Pass => $_) }, "Password must be text", "Pass non-string") for ["a"], JSON::XS::true;
like(JSON::XS->new->encode(ok_clean({ base(Pass => $_) }, "numeric Pass $_")), qr/"Pass":"\Q$_\E"/, "numeric Pass $_ stored as a string") for 12, 1.5;

# Channels
ok_clean({ base(Channels => [$_]) }, "Channel $_") for "#Foo-Bar", "&local", "##java", "+x", "!12345x", "#\x{e9}", "#" . ("a" x 199);
fails({ base(Channels => [$_]) }, "$_ isn't a valid channel name", "Channel '$_'") for "a", "#a b", "#a,#b", "#a\x07", "#" . ("a" x 200), "#";
fails({ base(Channels => "#a") }, "Channels must be a list", "Channels as a string");
fails({ base(Channels => [["#a"]]) }, "Channels must be a list", "Channels nested");

# Highlight
fails({ base(Highlight => "lee") }, "Highlight must be a list", "Highlight as a string");
fails({ base(Highlight => ["a\nb"]) }, "Highlight words can't contain control characters", "Highlight with a newline");

# Alias
my $alias_err = "Network alias can only use letters, digits and spaces, up to 32 characters";
is(ok_clean({ base(Alias => "Libera Chat 2") }, "Alias with spaces")->{Alias}, "Libera Chat 2", "Alias kept");
fails({ base(Alias => $_) }, $alias_err, "Alias '$_'") for "libera.chat", "a-b", "a" x 33;

# blocked_ip
ok($C->blocked_ip($_), "$_ blocked") for qw(
  127.0.0.1 10.0.0.1 100.64.0.1 100.127.255.255 169.254.1.1 172.16.0.1
  172.31.255.255 192.168.1.1 192.0.0.1 198.18.0.1 198.19.255.255 0.0.0.0
  224.0.0.1 255.255.255.255 :: ::1 fd00::1 fe80::1 ff02::1 64:ff9b::1
  ::ffff:127.0.0.1 ::ffff:10.1.2.3
);
ok(!$C->blocked_ip($_), "$_ allowed") for qw(
  8.8.8.8 100.63.255.255 100.128.0.0 172.15.255.255 172.32.0.0
  198.17.255.255 198.20.0.0 192.0.1.1 2001:4860::8888 ::ffff:8.8.8.8
);

# check_host
sub check {
  my ($host, $port, @ips) = @_;
  my $called = 0;
  my $ok = eval {
    $C->check_host($host, $port, resolve => sub { $called++; @ips });
  };
  my $err = $@;
  $err =~ s/ at \S+ line \d+.*//s;
  $err =~ s/\n\z//;
  return ($ok, $err, $called);
}

{
  local $ENV{DEFAULT_NETWORK_HOST};
  local $ENV{DEFAULT_NETWORK_PORT};
  my ($ok, $err) = check("a.com", 6667, "8.8.8.8");
  ok($ok, "public address allowed");
  ($ok, $err) = check("a.com", 6667, "8.8.8.8", "127.0.0.1");
  is($err, "Host a.com points to a private address", "any private address refuses");
  ($ok, $err) = check("a.com", 6667, "::ffff:127.0.0.1");
  is($err, "Host a.com points to a private address", "mapped loopback refuses");
  ($ok, $err) = check("nosuch.invalid", 6667);
  is($err, "Couldn't find host nosuch.invalid", "unresolvable");
  my $called;
  ($ok, $err, $called) = check("127.0.0.1", 6667, "8.8.8.8");
  is($err, "Host 127.0.0.1 points to a private address", "literal checked directly");
  is($called, 0, "literal not resolved");
  ($ok, $err) = check("ergo", 6667, "172.18.0.5");
  is($err, "Host ergo points to a private address", "nothing exempt when DEFAULT_NETWORK_HOST is unset");
}
{
  local $ENV{DEFAULT_NETWORK_HOST} = "IRC.relaychat.party";
  local $ENV{DEFAULT_NETWORK_PORT};
  my ($ok, $err, $called) = check("irc.relaychat.party", 6667, "127.0.0.1");
  ok($ok, "default host on default port is exempt");
  is($called, 0, "exempt host not resolved");
  ($ok, $err) = check("irc.relaychat.party", 5005, "127.0.0.1");
  is($err, "Host irc.relaychat.party points to a private address", "default host on another port refused");
}
{
  local $ENV{DEFAULT_NETWORK_HOST} = "ergo";
  local $ENV{DEFAULT_NETWORK_PORT} = "6697";
  my ($ok) = check("ergo", 6697, "172.18.0.5");
  ok($ok, "DEFAULT_NETWORK_PORT respected");
  my (undef, $err) = check("ergo", 6667, "172.18.0.5");
  is($err, "Host ergo points to a private address", "default port no longer exempt");
}
{
  local $ENV{DEFAULT_NETWORK_HOST};
  my $ok = eval { $C->check_host("localhost", 6667) };
  like($@, qr/^Host localhost points to a private address/, "real resolver: localhost");
}

done_testing;
