package Util;

use JSON::XS;
use Data::UUID;
use Time::HiRes ();
use Digest;
use Crypt::Bcrypt ();
use Math::BaseConvert;

sub event {
  my ($class, $type, $data, $id) = @_;

  return sprintf "event: %s\ndata: %s\nid: %s\n\n", $type, $data, $id
    if $id;

  return sprintf "event: %s\ndata: %s\n\n", $type, $data;
}

sub hash_password {
  my ($class, $password, $secret) = @_;

  my $secret = sprintf("%16s", $secret);
  my $bcrypt = Digest->new(
    'Bcrypt',
    cost => 15,
    salt => $secret
  );
  $bcrypt->add($password);
  $bcrypt->hexdigest;
}

# Standard bcrypt: a random salt per hash, cost 12, as a $2b$12$... string
# for the password_hash column. hash_password above is the legacy scheme,
# kept only to check old hashes.
sub new_password_hash {
  my ($class, $password) = @_;
  return Crypt::Bcrypt::bcrypt($password, "2b", 12, random_bytes(16));
}

sub check_password_hash {
  my ($class, $password, $hash) = @_;
  return 0 unless defined $password && defined $hash && $hash =~ /^\$2[ab]\$/;
  return eval { Crypt::Bcrypt::bcrypt_check($password, $hash) } ? 1 : 0;
}

# Compares two strings in time that doesn't depend on where they differ.
sub secure_eq {
  my ($class, $a, $b) = @_;
  return 0 unless defined $a && defined $b && length $a == length $b;
  my $diff = 0;
  $diff |= ord(substr $a, $_, 1) ^ ord(substr $b, $_, 1) for 0 .. length($a) - 1;
  return $diff == 0 ? 1 : 0;
}

sub random_bytes {
  my $n = shift;
  open my $fh, '<:raw', '/dev/urandom' or die "/dev/urandom: $!\n";
  my $got = read $fh, my $bytes, $n;
  die "short read from /dev/urandom\n" unless defined $got && $got == $n;
  return $bytes;
}

# A nick for the default network from a username ([a-zA-Z0-9_.]): nicks
# can't contain "." or start with a digit, and our Ergo allows 32 characters.
sub irc_nick {
  my ($class, $username) = @_;
  (my $nick = $username) =~ tr/./_/;
  $nick = "_$nick" if $nick =~ /^[0-9]/;
  return substr $nick, 0, 32;
}

sub uuid {
  return cnv(Data::UUID->new->create_hex, 16, 62);
}

sub irc_event {
  my ($class, $id, $prefix, $command, @params) = @_;
  $class->event(irc => encode_json {
    ConnectionId => $id,
    Message => {
      Command => $command,
      Prefix  => {Name => $prefix},
      Params  => [@params],
      Time    => Time::HiRes::time,
    }
  });
}

1;
