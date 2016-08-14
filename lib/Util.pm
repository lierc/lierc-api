package Util;

use Data::UUID;
use Digest;
use Math::BaseConvert;

sub event {
  my ($class, $type, $data, $id) = @_;
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

sub uuid {
  return cnv(Data::UUID->new->create_hex, 16, 62);
}

1;
