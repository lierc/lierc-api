package Util;

use JSON::XS;
use Data::UUID;
use Time::HiRes ();
use Digest;
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
