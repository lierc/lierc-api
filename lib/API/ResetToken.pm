package API::ResetToken;

# Signed password reset tokens. Nothing is stored: the token carries the user
# id and an expiry, and its MAC covers the user's current password hash, so
# it stops verifying once the password changes, which makes it single use.

use strict;
use warnings;

use Digest::SHA qw(hmac_sha256_hex);
use MIME::Base64 qw(encode_base64url decode_base64url);
use Util;

use constant LIFETIME => 3600;

sub mac {
  my (%args) = @_;
  return hmac_sha256_hex(
    join("|", "reset", @args{qw(user expires fingerprint)}),
    $args{secret},
  );
}

sub make {
  my ($class, %args) = @_;
  return encode_base64url(join ":", $args{user}, $args{expires}, mac(%args));
}

# (user, expires, mac) from a token, or nothing if it isn't one
sub parts {
  my $token = shift;
  return unless defined $token && $token =~ /^[A-Za-z0-9_-]+$/;
  my @parts = split /:/, decode_base64url($token), -1;
  return unless @parts == 3 && length $parts[0] && $parts[1] =~ /^\d+$/;
  return @parts;
}

# The user id the token names, unverified: for looking the user up.
sub user {
  my ($class, $token) = @_;
  my ($user) = parts($token);
  return $user;
}

sub valid {
  my ($class, %args) = @_;
  my ($user, $expires, $mac) = parts($args{token}) or return 0;
  return 0 if $args{now} > $expires;
  my $want = mac(
    secret      => $args{secret},
    user        => $user,
    expires     => $expires,
    fingerprint => $args{fingerprint},
  );
  return Util->secure_eq($mac, $want);
}

1;
