use strict;
use warnings;

use Test::More;
use API::ResetToken;

my %k = (secret => "s3cret", user => "abc123", fingerprint => '$2b$12$old');
my $t = API::ResetToken->make(%k, expires => 1000 + 3600);

like($t, qr/^[A-Za-z0-9_-]+$/, "URL-safe");
is(API::ResetToken->user($t), "abc123", "carries the user id");

my %v = (secret => "s3cret", token => $t, fingerprint => '$2b$12$old');
ok( API::ResetToken->valid(%v, now => 1000), "valid");
ok( API::ResetToken->valid(%v, now => 1000 + 3600), "valid until its expiry");
ok(!API::ResetToken->valid(%v, now => 1000 + 3601), "expired");
ok(!API::ResetToken->valid(%v, fingerprint => '$2b$12$new', now => 1000), "password changed since");
ok(!API::ResetToken->valid(%v, secret => "other", now => 1000), "wrong secret");

(my $bad = $t) =~ s/^./ $& eq "A" ? "B" : "A" /e;
ok(!API::ResetToken->valid(%v, token => $bad, now => 1000), "tampered");
ok(!API::ResetToken->valid(%v, token => "garbage", now => 1000), "garbage");
ok(!API::ResetToken->valid(%v, token => undef, now => 1000), "no token");
is(API::ResetToken->user("garbage"), undef, "no user from garbage");
is(API::ResetToken->user(undef), undef, "no user from undef");
is(API::ResetToken::LIFETIME, 3600, "lifetime is an hour");

done_testing;
