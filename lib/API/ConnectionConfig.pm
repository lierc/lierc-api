package API::ConnectionConfig;

# Checks and cleans an IRC connection's settings before they're saved and
# sent to liercd, and refuses hosts that point at loopback or private
# addresses. Every check dies with a message for the user.

use strict;
use warnings;

use JSON::XS;
use Socket qw(AF_INET AF_INET6 SOCK_STREAM getaddrinfo inet_pton inet_ntop
              unpack_sockaddr_in unpack_sockaddr_in6);

my $NICK_CHARS = q{A-Za-z0-9\[\]\\\\`_^{|}-};   # for User

# A nick may use any characters (servers decide what they allow) except
# whitespace, control characters and , * ? ! @, and can't start with a
# channel prefix (# &) or $ or :. Whitespace is listed rather than \s so it
# matches internal/connconfig exactly.
my $SPACE = q{\t\n\x0b\f\r \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}};
my $NICK_NOT = qq{${SPACE}\\x00-\\x1f\\x7f-\\x9f,*?!\@};
my $NICK_RE = qr/^[^${NICK_NOT}#&\$:][^${NICK_NOT}]{0,31}\z/;

# Returns a new hashref with only the known keys, trimmed (except Pass) and
# normalised; dies on the first invalid field.
sub clean {
  my ($class, $params) = @_;
  die "Host is required\n" unless ref $params eq "HASH";

  my %config;

  my $host = text($params->{Host});
  die "Host is required\n" if !defined $params->{Host} || (defined $host && $host eq "");
  die "Host must be an IPv4 address or a domain name\n" unless defined $host && valid_host($host);
  $config{Host} = $host;

  my $port = $params->{Port};
  die "Port must be a number from 1 to 65535\n"
    unless defined $port && !ref $port && $port =~ /^\s*(\d{1,5})\s*$/a && $1 >= 1 && $1 <= 65535;
  $config{Port} = int $1;

  my $nick = text($params->{Nick});
  die "Nick is required\n" if !defined $nick || $nick eq "";
  die "Nick must be 1 to 32 characters, without spaces or , * ? ! \@, and can't start with # & \$ or :\n"
    unless $nick =~ $NICK_RE;
  $config{Nick} = $nick;

  my $user = text($params->{User});
  if (defined $params->{User} && !(defined $user && $user eq "")) {
    die 'Username must be up to 32 letters, digits, - . ~ or [ ] \ ` _ ^ { | }' . "\n"
      unless defined $user && $user =~ /^[.~${NICK_CHARS}]{1,32}$/;
    $config{User} = $user;
  }

  if (defined(my $pass = $params->{Pass})) {
    die "Password must be text\n" if ref $pass;
    die "Password can't contain line breaks\n" if $pass =~ /[\r\n\0]/;
    # a JSON number becomes a string; liercd's Pass is a string
    $config{Pass} = "$pass" if $pass ne "";
  }

  $config{Channels} = [ map {
    die "$_ isn't a valid channel name\n"
      unless /^[#&+!][^\s,\x00-\x1f\x7f]{1,199}$/;
    $_;
  } list($params->{Channels}, "Channels") ];

  $config{Highlight} = [ map {
    die "Highlight words can't contain control characters\n" if /[\x00-\x1f\x7f]/;
    $_;
  } list($params->{Highlight}, "Highlight") ];

  my $alias = text($params->{Alias});
  if (defined $params->{Alias} && !(defined $alias && $alias eq "")) {
    die "Network alias can only use letters, digits and spaces, up to 32 characters\n"
      unless defined $alias && $alias =~ /^[A-Za-z0-9 ]{1,32}$/;
    $config{Alias} = $alias;
  }

  $config{Ssl}  = $params->{Ssl}  ? JSON::XS::true : JSON::XS::false;
  $config{SASL} = $params->{SASL} ? JSON::XS::true : JSON::XS::false;

  return \%config;
}

# A trimmed string, or undef for a missing or non-string value.
sub text {
  my $value = shift;
  return undef if !defined $value || ref $value;
  (my $trimmed = $value) =~ s/^\s+|\s+$//g;
  return $trimmed;
}

# The trimmed, non-blank strings of an optional list.
sub list {
  my ($value, $name) = @_;
  return () unless defined $value;
  die "$name must be a list\n" unless ref $value eq "ARRAY";
  return grep { $_ ne "" } map {
    die "$name must be a list\n" if !defined $_ || ref $_;
    text($_);
  } @$value;
}

sub ipv4_literal {
  my $host = shift;
  return $host =~ /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/a
    && !grep { $_ > 255 } $1, $2, $3, $4;
}

sub valid_host {
  my $host = shift;
  return 1 if ipv4_literal($host);
  return 0 if length $host > 253;
  my @labels = split /\./, $host, -1;
  return 0 if $labels[-1] =~ /^\d+$/;   # numeric but not a valid IPv4 address
  for (@labels) {
    return 0 unless /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
  }
  return 1;
}

# Addresses liercd must not connect to. The same list is in lierc's
# pkg/lierc/dialguard.go and lierc-api's internal/connconfig (Go, used by
# lctl); keep the three in step. The rule cases in
# testdata/connection-config-cases.json are shared with internal/connconfig.
my @BLOCKED_V4 = qw(
  0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16
  172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4
  240.0.0.0/4
);
my @BLOCKED_V6 = qw(
  ::/128 ::1/128 fc00::/7 fe80::/10 ff00::/8 64:ff9b::/96
);

my @blocked = (
  (map { prefix(AF_INET, $_) } @BLOCKED_V4),
  (map { prefix(AF_INET6, $_) } @BLOCKED_V6),
);

sub prefix {
  my ($family, $cidr) = @_;
  my ($addr, $bits) = split m{/}, $cidr;
  return [ $family, inet_pton($family, $addr), $bits ];
}

sub in_prefix {
  my ($packed, $net, $bits) = @_;
  my $bytes = int($bits / 8);
  return 0 if substr($packed, 0, $bytes) ne substr($net, 0, $bytes);
  my $rest = $bits % 8 or return 1;
  my $mask = (0xff << (8 - $rest)) & 0xff;
  return (ord(substr($packed, $bytes, 1)) & $mask) == (ord(substr($net, $bytes, 1)) & $mask);
}

sub blocked_ip {
  my ($class, $ip) = @_;
  my $family = $ip =~ /:/ ? AF_INET6 : AF_INET;
  my $packed = inet_pton($family, $ip);
  return 1 unless defined $packed;   # not an address: refuse
  # an IPv4-mapped IPv6 address is checked as the IPv4 address it carries
  if ($family == AF_INET6 && substr($packed, 0, 12) eq ("\0" x 10) . "\xff\xff") {
    $family = AF_INET;
    $packed = substr($packed, 12);
  }
  for (@blocked) {
    my ($f, $net, $bits) = @$_;
    return 1 if $f == $family && in_prefix($packed, $net, $bits);
  }
  return 0;
}

# The default network (DEFAULT_NETWORK_HOST on DEFAULT_NETWORK_PORT) may be
# private, e.g. irc.relaychat.party -> 127.0.0.1 in /etc/hosts.
sub exempt {
  my ($class, $host, $port) = @_;
  my $default = $ENV{DEFAULT_NETWORK_HOST};
  return 0 unless defined $default && $default ne "";
  return lc $host eq lc $default
    && $port == int($ENV{DEFAULT_NETWORK_PORT} || 6667);
}

sub resolve {
  my $host = shift;
  my ($err, @res) = getaddrinfo($host, undef, { socktype => SOCK_STREAM });
  return () if $err;
  return map {
    $_->{family} == AF_INET6
      ? inet_ntop(AF_INET6, (unpack_sockaddr_in6($_->{addr}))[1])
      : inet_ntop(AF_INET,  (unpack_sockaddr_in($_->{addr}))[1]);
  } grep { $_->{family} == AF_INET || $_->{family} == AF_INET6 } @res;
}

# Dies unless the host is exempt or every address it resolves to is allowed.
sub check_host {
  my ($class, $host, $port, %opt) = @_;
  return 1 if $class->exempt($host, $port);
  my @ips = ipv4_literal($host) ? ($host) : ($opt{resolve} || \&resolve)->($host);
  die "Couldn't find host $host\n" unless @ips;
  die "Host $host points to a private address\n" if grep { $class->blocked_ip($_) } @ips;
  return 1;
}

1;
