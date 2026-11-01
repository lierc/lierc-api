use strict;
use warnings;

use Test::More;
use Digest::SHA qw(hmac_sha256_hex);
use MIME::Base64 qw(encode_base64url);
use API::SignedToken;

# reset tokens: every case the old ResetToken tests covered
my %k = (purpose => "reset", secret => "s3cret", user => "abc123", fingerprint => '$2b$12$old');
my $t = API::SignedToken->make(%k, expires => 1000 + 3600);

like($t, qr/^[A-Za-z0-9_-]+$/, "URL-safe");
is(API::SignedToken->user($t), "abc123", "carries the user id");

my %v = (purpose => "reset", secret => "s3cret", token => $t, fingerprint => '$2b$12$old');
ok( API::SignedToken->valid(%v, now => 1000), "valid");
ok( API::SignedToken->valid(%v, now => 1000 + 3600), "valid until its expiry");
ok(!API::SignedToken->valid(%v, now => 1000 + 3601), "expired");
ok(!API::SignedToken->valid(%v, fingerprint => '$2b$12$new', now => 1000), "password changed since");
ok(!API::SignedToken->valid(%v, secret => "other", now => 1000), "wrong secret");

(my $bad = $t) =~ s/^./ $& eq "A" ? "B" : "A" /e;
ok(!API::SignedToken->valid(%v, token => $bad, now => 1000), "tampered");
ok(!API::SignedToken->valid(%v, token => "garbage", now => 1000), "garbage");
ok(!API::SignedToken->valid(%v, token => undef, now => 1000), "no token");
is(API::SignedToken->user("garbage"), undef, "no user from garbage");
is(API::SignedToken->user(undef), undef, "no user from undef");

# verify tokens, and purposes don't mix
my $vt = API::SignedToken->make(purpose => "verify", secret => "s", user => "u1", fingerprint => 'a@b.c', expires => 5000);
ok( API::SignedToken->valid(purpose => "verify", secret => "s", token => $vt, fingerprint => 'a@b.c', now => 4000), "verify token valid");
ok(!API::SignedToken->valid(purpose => "reset",  secret => "s", token => $vt, fingerprint => 'a@b.c', now => 4000), "verify token is not a reset token");
my $rt = API::SignedToken->make(purpose => "reset", secret => "s", user => "u1", fingerprint => 'a@b.c', expires => 5000);
ok(!API::SignedToken->valid(purpose => "verify", secret => "s", token => $rt, fingerprint => 'a@b.c', now => 4000), "reset token is not a verify token");
ok(!API::SignedToken->valid(purpose => "verify", secret => "s", token => $vt, fingerprint => 'new@b.c', now => 4000), "email changed");

# reset tokens already sent keep working: the format is unchanged
my $old = encode_base64url("u1:5000:" . hmac_sha256_hex("reset|u1|5000|fp", "s"));
is(API::SignedToken->make(purpose => "reset", secret => "s", user => "u1", fingerprint => "fp", expires => 5000), $old, "reset format unchanged");

is(API::SignedToken::RESET_LIFETIME, 3600, "reset lasts an hour");
is(API::SignedToken::VERIFY_LIFETIME, 30 * 86400, "verify lasts 30 days");

done_testing;
