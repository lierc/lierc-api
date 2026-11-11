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

# ---- legacy_hashes.json -------------------------------------------------
# The legacy scheme, Util->hash_password: bcrypt cost 15 with the secret
# (left-padded to 16 bytes) as the salt, as a hex digest. Passwords are
# UTF-8 bytes.
require Encode;
my @legacy;
for my $case (
  ["0123456789abcdef", "correct horse"],
  ["0123456789abcdef", "p\x{e4}ssw\x{f6}rd"],
  ["0123456789abcdef", "x" x 80],
  ["changeme",         "correct horse"],
) {
  my ($s, $p) = @$case;
  my $bytes = Encode::encode("UTF-8", $p);
  push @legacy, {
    secret   => $s,
    password => $p,
    legacy   => Util->hash_password($bytes, $s),
  };
}
write_json("legacy_hashes.json", \@legacy);

# ---- auth.json ----------------------------------------------------------
# API::SignedToken, Data::Validate::Email::is_email (called as Auth.pm does,
# with no options) and Util->irc_nick. Also writes internal/accounts/tlds.go
# from Net::Domain::TLD's list.
require API::SignedToken;
require Data::Validate::Email;
require Net::Domain::TLD;

# tokens
my $tsecret = "test-secret";
my $expires = 1800000000;
my @made;
my @valid;
for my $t (
  [verify => "aB3dE5fG7hJ9kL1mN3pQ5r", 'lee@example.com'],
  [reset  => "aB3dE5fG7hJ9kL1mN3pQ5r",
    '$2b$15$abcdefghijklmnopqrstuuJ1mY2Vh7pN8kS0QwErTyUiOpAsDfGhK'],
  [verify => "12345", 'a.b+c@example.co.uk'],
  [reset  => "12345", 'deadbeef' x 8],
) {
  my ($purpose, $user, $fp) = @$t;
  my $token = API::SignedToken->make(
    secret => $tsecret, purpose => $purpose, user => $user,
    fingerprint => $fp, expires => $expires,
  );
  push @made, {
    secret => $tsecret, purpose => $purpose, user => $user,
    fingerprint => $fp, expires => $expires, token => $token,
  };
  my $other = $purpose eq 'verify' ? 'reset' : 'verify';
  for my $c (
    ["right",             $tsecret,        $purpose, $fp,       $expires - 3600],
    ["right_at_expiry",   $tsecret,        $purpose, $fp,       $expires],
    ["expired",           $tsecret,        $purpose, $fp,       $expires + 1],
    ["wrong_purpose",     $tsecret,        $other,   $fp,       $expires - 3600],
    ["wrong_fingerprint", $tsecret,        $purpose, "$fp.x",   $expires - 3600],
    ["wrong_secret",      "other-secret",  $purpose, $fp,       $expires - 3600],
  ) {
    my ($name, $s, $p, $f, $now) = @$c;
    push @valid, {
      name => "$purpose/$user/$name", secret => $s, purpose => $p,
      token => $token, fingerprint => $f, now => $now,
      valid => API::SignedToken->valid(
        secret => $s, purpose => $p, token => $token,
        fingerprint => $f, now => $now,
      ) ? JSON::XS::true : JSON::XS::false,
    };
  }
}

# Odd tokens: what user() names, and whether valid() takes them, for
# purpose verify, fingerprint lee@example.com, now = $expires - 3600.
require MIME::Base64;
my $b64u = sub { MIME::Base64::encode_base64url($_[0]) };
my $good = $made[0]{token};
my @parsing;
for my $token (
  "", "!!!!", "abc def", $good, "${good}A", "${good}AA", "$good=",
  $b64u->("lee"),
  $b64u->("lee:1800000000"),
  $b64u->("lee:1800000000:mac:extra"),
  $b64u->(":1800000000:mac"),
  $b64u->("lee:18x:mac"),
  $b64u->("lee::mac"),
  $b64u->("lee:1800000000:"),
  $b64u->("lee:-1:mac"),
  $b64u->("lee:1800000000:mac\n"),
  $b64u->("lee:1800000000\n:mac"),
  # properly signed, with odd expires: Perl's /^\d+$/ takes a trailing
  # newline, and its numeric compare takes more digits than an int64
  map {
    API::SignedToken->make(
      secret => $tsecret, purpose => "verify", user => "lee",
      fingerprint => 'lee@example.com', expires => $_,
    )
  } ("1800000000\n", "1800000000\n\n", "0001800000000", "9" x 30,
     $expires - 3600, $expires - 3601),
) {
  my $u = API::SignedToken->user($token);
  push @parsing, {
    token => $token,
    user  => $u,
    valid => API::SignedToken->valid(
      secret => $tsecret, purpose => "verify", token => $token,
      fingerprint => 'lee@example.com', now => $expires - 3600,
    ) ? JSON::XS::true : JSON::XS::false,
  };
}

# emails. Data::Validate::Email ships no test file in local/, so these cases
# follow its documentation and regexes (local part /^[a-z0-9_\-\.\+]+$/i,
# Data::Validate::Domain labels and the Net::Domain::TLD list).
sub domain_of_length {
  # a domain of exactly $n characters ending in .com
  my $n = shift;
  my @labels;
  my $left = $n - 4;    # room before ".com"
  while ($left > 0) {
    my $len = $left > 64 ? 63 : $left == 64 ? 62 : $left;
    push @labels, "a" x $len;
    $left -= $len + 1;    # label plus its dot
  }
  my $d = join(".", @labels) . ".com";
  die "domain_of_length($n) gave " . length($d) unless length $d == $n;
  return $d;
}
my @email_cases = (
  # documented: something@something.tld
  'bob@test.com', 'bob@test.com.au', 'foo.bob@test.com.au',
  'foo-bob@test-com.au', 'foo-bob@test.uk', 'bob_smith@example.org',
  'a+tag@example.com', 'A.B+C@Example.COM', '0@example.com',
  '1234@5678.net', 'x@a.io', 'x@xn--bcher-kva.com',
  # documented: no real names, comments or partial addresses
  'Richard Sonnen <sonnen@frii.com>', '<sonnen@frii.com>',
  'sonnen@frii.com (Richard Sonnen)', 'sonnen@frii', 'bob', '', '@',
  'bob@', '@example.com', 'bob@@example.com', 'a@b@example.com',
  'bob@localhost', 'bob@com',
  # local part
  'bob smith@example.com', 'bob,smith@example.com', 'bob!@example.com',
  "bob'o\@example.com", 'bob/x@example.com', 'bob=x@example.com',
  ".bob\@example.com", "bob.\@example.com", 'bo..b@example.com',
  "b\x{e9}b\@example.com", "bob\n\@example.com",
  # quoted local parts
  '"bob"@example.com', '"bob smith"@example.com', '"a@b"@example.com',
  # domain labels
  'bob@-example.com', 'bob@example-.com', 'bob@ex-ample.com',
  'bob@ex--ample.com', 'bob@ex_ample.com', 'bob@exa mple.com',
  'bob@.example.com', 'bob@example..com', 'bob@example.com.',
  'bob@example.com..', 'bob@example.', 'bob@.', "bob\@example.com\n",
  "bob\@ex\x{e4}mple.com", 'bob@1.2.3.4', 'bob@[1.2.3.4]',
  'bob@[IPv6:::1]', 'bob@127.0.0.1.com',
  'bob@' . ("a" x 63) . '.com', 'bob@' . ("a" x 64) . '.com',
  'bob@' . ("a" x 65) . '.com',
  'bob@' . domain_of_length(253), 'bob@' . domain_of_length(254),
  'bob@' . domain_of_length(253) . '.', 'bob@' . domain_of_length(254) . '.',
  # TLDs
  'a@example.test', 'a@foo.localhost', 'a@x.inc', 'a@x.museum', 'a@x.zz',
  'a@x.MUSEUM', 'a@x.example', 'a@x.invalid', 'a@x.xn--p1ai',
  'a@x.123', 'a@x.c',
  # trailing @: Perl's split drops the empty last field (Go refuses)
  'a@b.com@', 'a@b.com@@',
);
my @emails = map {
  +{ email => $_,
     valid => defined(Data::Validate::Email::is_email($_))
       ? JSON::XS::true : JSON::XS::false }
} @email_cases;

# nicks
my @nicks = map { +{ username => $_, nick => Util->irc_nick($_) } }
  ("lee", "9lives", "a.b", "\x{e9}", "abcdefghij" x 4, "_x", "Lee.Do_1");

write_json("auth.json", {
  tokens => { make => \@made, valid => \@valid, parsing => \@parsing },
  emails => \@emails,
  nicks  => \@nicks,
});

# tlds.go: every name tld_exists accepts. Only names that can pass
# Data::Validate::Domain's ASCII label check are written; the rest
# (the non-ASCII IDN names) can never be reached.
# tlds() lists a name once per category it's in, so dedupe.
my %tld_seen;
my @tlds = sort grep { !$tld_seen{$_}++ }
  grep { /^[a-z0-9-]+$/ && Net::Domain::TLD::tld_exists($_) }
  map { lc } Net::Domain::TLD::tlds();
my $go = File::Spec->catfile($dir, File::Spec->updir, File::Spec->updir,
  "internal", "accounts", "tlds.go");
open my $gofh, '>', $go or die "$go: $!";
print $gofh "// Code generated by testdata/fixtures/gen.pl from Net::Domain::TLD "
  . "$Net::Domain::TLD::VERSION; DO NOT EDIT.\n\n";
print $gofh "package accounts\n\n";
print $gofh "// tlds is Net::Domain::TLD's list, lowercased: the names tld_exists accepts.\n";
print $gofh "var tlds = map[string]bool{\n";
print $gofh "\t\"$_\": true,\n" for @tlds;
print $gofh "}\n";
close $gofh;
# gofmt aligns the values; without it the file is valid Go, just unformatted.
system("gofmt", "-w", $go) == 0 or warn "gofmt $go failed; run it by hand\n";
print "wrote $go\n";
