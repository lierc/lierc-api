#!/usr/bin/env perl
# Generates the golden fixtures in this directory from the real Perl code.
# Setup and usage: see README.md. Run from the repository root:
#   perl -Itestdata/fixtures/local/lib/perl5 -Ilib testdata/fixtures/gen.pl
use strict;
use warnings;
use utf8;
use FindBin;
use File::Spec;
use JSON::XS ();
use HTTP::Request::Common qw(GET);
use Plack::Builder;
use Plack::Test;
use Math::BaseConvert qw(cnv);
use Util;

my $dir = $FindBin::Bin;
my $json = JSON::XS->new->utf8->canonical->pretty;

sub write_json {
  my ($name, $data) = @_;
  my $path = File::Spec->catfile($dir, $name);
  open my $fh, '>', $path or die "$path: $!";
  print $fh $json->encode($data);
  close $fh;
  print "wrote $path\n";
}

# ---- cookies.json -------------------------------------------------------
# Same Session::Cookie options as bin/api.psgi (secure left off so the
# Set-Cookie header is the same shape for every case).
my $secret = "test-secret";
my %seen_ser;
my $mw_session;
my $app_builder = sub {
  my $inner = shift;
  builder {
    enable "Session::Cookie",
      secret      => $secret,
      expires     => 3600 * 24 * 7,
      httponly    => 1,
      secure      => 0,
      session_key => "chats";
    $inner;
  };
};

sub set_cookie_of {
  my ($res) = @_;
  my $h = $res->header('Set-Cookie');
  return $h;
}

sub cookie_value_of {
  my ($header) = @_;
  return undef unless defined $header;
  my ($v) = $header =~ /^chats=([^;]*)/;
  return $v;
}

# Decode the session the way the middleware itself does, from the cookie value.
sub decode_session {
  my ($value) = @_;
  require URI::Escape;
  require MIME::Base64;
  require Storable;
  # cookie payload is "<id>:<base64 Storable>:<hmac_sha1_hex>"
  my (undef, $b64, undef) = split /:/, URI::Escape::uri_unescape($value), 3;
  return Storable::thaw(MIME::Base64::decode($b64));
}

my @cookies;
sub record {
  my ($name, $app, @headers) = @_;
  my $res;
  test_psgi $app_builder->($app), sub {
    my $cb = shift;
    $res = $cb->(GET "/", @headers);
  };
  my $header = set_cookie_of($res);
  my $value  = cookie_value_of($header);
  die "$name: no cookie issued\n" unless defined $value;
  push @cookies, {
    name              => $name,
    session           => decode_session($value),
    set_cookie_header => $header,
    cookie_value      => $value,
  };
  return $value;
}

record login_ascii => sub {
  $_[0]{'psgix.session'}{user} = "lee";
  [200, [], ["ok"]];
};
record login_numeric_id => sub {
  $_[0]{'psgix.session'}{user} = "12345";
  [200, [], ["ok"]];
};
record login_non_ascii => sub {
  # character string, so Storable records the UTF-8 flag
  $_[0]{'psgix.session'}{user} = "p\x{e4}ssw\x{f6}rd\x{263a}";
  [200, [], ["ok"]];
};
record login_email_like => sub {
  $_[0]{'psgix.session'}{user} = 'a.b+c@example.com';
  [200, [], ["ok"]];
};
record empty_session => sub {
  my $s = $_[0]{'psgix.session'};
  $s->{tmp} = 1;
  delete $s->{tmp};
  [200, [], ["ok"]];
};

# Logout: log in, then send the cookie back to an app that deletes `user`.
my $logged_in = record logout_setup => sub {
  $_[0]{'psgix.session'}{user} = "lee";
  [200, [], ["ok"]];
};
pop @cookies;    # the setup request is not a fixture itself
record logout_after_delete => sub {
  my $s = $_[0]{'psgix.session'};
  die "session not restored\n" unless ($s->{user} || '') eq 'lee';
  delete $s->{user};
  [200, [], ["ok"]];
}, Cookie => "chats=$logged_in";

write_json("cookies.json", \@cookies);

# ---- passwords.json -----------------------------------------------------
# Util->hash_password gets the raw bytes from the form, so non-ASCII
# passwords are UTF-8 encoded before hashing. The JSON stores them as UTF-8
# strings; Go must hash the UTF-8 bytes of the JSON string.
require Encode;
my @secrets   = ("changeme", "exactly16chars!!", "fifteen_chars!!");
# Secrets longer than 16 bytes are not fixtures: Digest::Bcrypt dies with
# "Salt must be exactly 16 octets long" (see README), so assert that instead.
eval { Util->hash_password("x", "a-secret-longer-than-sixteen") };
die "expected long secret to die\n" unless $@ =~ /Salt must be exactly 16 octets/;
my @passwords = (
  "",
  "hunter2",
  "p\x{e4}ssw\x{f6}rd",
  "a" x 72,
  join("", map { chr(ord('a') + $_ % 26) } 0 .. 79),
);
my @pw;
for my $s (@secrets) {
  for my $p (@passwords) {
    my $bytes = Encode::encode("UTF-8", $p);
    push @pw, {
      secret   => $s,
      password => $p,
      hash     => Util->hash_password($bytes, $s),
    };
  }
}
write_json("passwords.json", \@pw);

# ---- ids.json -----------------------------------------------------------
my @hex = (
  "0" x 32,
  "f" x 32,
  "0" x 31 . "1",
  "0" x 30 . "ff",
  "0" x 24 . "deadbeef",
  "00000000000000000000000000000010",
  "1" . "0" x 31,
  "8" . "0" x 31,
  "0123456789abcdef0123456789abcdef",
  "fedcba9876543210fedcba9876543210",
  "000102030405060708090a0b0c0d0e0f",
  "a" x 32,
  "0a" . "0" x 30,
  "00ffffffffffffffffffffffffffffff",
  "7fffffffffffffffffffffffffffffff",
  "ffffffff" . "0" x 24,
  "0000000100000001" . "0" x 16,
  "6ba7b8109dad11d180b400c04fd430c8",
  "550e8400e29b41d4a716446655440000",
  "123e4567e89b12d3a456426614174000",
);
my @cnv = map { +{ hex => $_, b62 => scalar cnv($_, 16, 62) } } @hex;
my @uuids = map { Util->uuid } 1 .. 5;
write_json("ids.json", { cnv => \@cnv, uuid_samples => \@uuids });

# ---- emails.json --------------------------------------------------------
# Data::Validate::Email::is_email as called by Auth.pm register.
require Data::Validate::Email;
my @emails = (
  'a@example.com', 'first.last@example.com', 'user+tag@sub.example.co.uk',
  q{o'brien@example.org}, 'x_y-z@ex-ample.net', '1234@example.io',
  'UPPER@EXAMPLE.COM', '', 'plain', '@example.com', 'a@', 'a@example',
  'a@.com', 'a@example..com', 'a b@example.com', 'a@exa mple.com',
  '.a@example.com', 'a.@example.com', 'a..b@example.com', 'a@-example.com',
  'a@example-.com', 'a@example.c0m', 'a@b@example.com', "a\@example.com\n",
  '<a@example.com>', 'a@example.c',
  # TLDs
  'lee@relaychat.party', 'a@example.org', 'a@example.uk', 'a@example.de',
  'a@example.museum', 'a@example.xyz', 'a@example.zzzz', 'a@example.invalid',
  'a@example.local', 'a@EXAMPLE.PARTY', 'a@example.Com',
  # subdomains, trailing dot, underscores
  'a@mail.sub.example.com', 'a@example.com.', 'a@example.com..', 'a@a_b.example.com',
  'a_b@example.com', 'a@1.example.com',
  # IP literals and bare IPs
  'a@[127.0.0.1]', 'a@127.0.0.1', 'a@[IPv6:::1]',
  # quoted local parts, odd characters
  '"quoted"@example.com', '"a b"@example.com', 'a!b@example.com', 'a#b@example.com',
  'a/b@example.com', 'a&b@example.com', 'a%b@example.com', 'a=b@example.com',
  # plus tags
  'user+tag@example.com', '+@example.com', 'a+b+c@example.com',
  # whitespace
  ' a@example.com', 'a@example.com ', "a\@example.com\t", "\na\@example.com",
  # extra @ and split quirks
  'a@example.com@', '@@', 'a@@example.com', 'a@b@',
  # label lengths
  'a@' . ('x' x 63) . '.com', 'a@' . ('x' x 64) . '.com', ('x' x 70) . '@example.com',
  'a@' . join('.', ('abcdefghi') x 30) . '.com',
  '0@0.com', 'a@a.co',
);
write_json("emails.json", [ map {
  +{ email => $_, valid => (defined Data::Validate::Email::is_email($_) ? JSON::XS::true : JSON::XS::false) }
} @emails ]);

# ---- Net::Domain::TLD list ----------------------------------------------
# internal/api/tlds.go: the TLDs tld_exists() accepts.
require Net::Domain::TLD;
{
  # Keys that can never match a valid domain label (HTML-entity or raw-byte IDN
  # names in the module's table) are dropped.
  my %uniq;
  my @tlds = sort { $a cmp $b } grep { /\A[0-9a-z-]+\z/ && !$uniq{$_}++ } Net::Domain::TLD::tlds();
  my $go = "// Code generated by testdata/fixtures/gen.pl from Net::Domain::TLD $Net::Domain::TLD::VERSION; DO NOT EDIT.\n"
         . "// Regenerate: see testdata/fixtures/README.md.\n\n"
         . "package api\n\n// knownTLDs is the lowercase TLD set of Net::Domain::TLD::tld_exists.\n"
         . "var knownTLDs = map[string]bool{\n";
  $go .= join('', map { sprintf("\t%s: true,\n", JSON::XS->new->utf8->encode("$_")) } @tlds);
  $go .= "}\n";
  my $path = File::Spec->catfile($dir, File::Spec->updir, File::Spec->updir, 'internal', 'api', 'tlds.go');
  open my $fh, '>', $path or die "$path: $!";
  print $fh $go;
  close $fh;
  print "wrote $path (" . scalar(@tlds) . " TLDs)\n";
}
