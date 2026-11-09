package API::Controller::WebPush;

use parent 'API::Controller';

use MIME::Base64 qw(decode_base64url);

# The push services browsers use. The notifier POSTs to whatever endpoint
# is saved, so only these are accepted; it also refuses private addresses
# when sending (lierc pkg/notify/pushclient.go).
my @PUSH_HOSTS = (
  qr/fcm\.googleapis\.com/,                       # Chrome, Edge, Opera, Samsung
  qr/updates\.push\.services\.mozilla\.com/,      # Firefox
  qr/(?:[a-z0-9-]+\.)+notify\.windows\.com/,        # legacy Edge
  qr/(?:[a-z0-9-]+\.)*push\.apple\.com/,           # Safari
);

# check_subscription returns why a subscription can't be saved, or undef.
# The endpoint must be https on a known push service; key and auth must be
# the browser's P-256 public key (65 bytes, uncompressed) and 16 byte auth
# secret, base64url encoded.
sub check_subscription {
  my ($endpoint, $key, $auth) = @_;

  return "endpoint is required" unless defined $endpoint && length $endpoint;
  return "endpoint is too long" if length $endpoint > 255;
  my ($host) = $endpoint =~ m{\Ahttps://([A-Za-z0-9.-]+)(?::443)?(?:/[!-~]*)?\z}
    or return "endpoint must be an https URL";
  $host = lc $host;
  return "endpoint is not a known push service"
    unless grep { $host =~ /\A$_\z/ } @PUSH_HOSTS;

  for ([key => $key, 65], [auth => $auth, 16]) {
    my ($name, $value, $size) = @$_;
    return "$name is required" unless defined $value && length $value;
    return "$name is not base64url" unless $value =~ /\A[A-Za-z0-9_-]+={0,2}\z/;
    my $bytes = decode_base64url($value);
    return "$name has the wrong length" unless length $bytes == $size;
    return "key is not an uncompressed P-256 key" if $name eq "key" && substr($bytes, 0, 1) ne "\x04";
  }

  return undef;
}

API->register("webpush.upsert", __PACKAGE__);
API->register("webpush.delete", __PACKAGE__);
API->register("webpush.keys",   __PACKAGE__);

sub keys {
  my ($app, $req) = @_;
  $app->json({
    vapid_public_key => $ENV{VAPID_PUBLIC},
  });
}

# DELETE /notification/web_push?endpoint=<url> forgets one subscription.
# The endpoint is in the query, not the path: nginx merges the slashes in a
# path, turning "https://" into "https:/".
sub delete {
  my ($app, $req) = @_;
  my $user     = $req->session->{user};
  my $endpoint = $req->query_parameters->{endpoint};

  return $app->error("endpoint is required") unless defined $endpoint && length $endpoint;

  my $sth = $app->dbh->prepare(q{
    DELETE FROM web_push
    WHERE "user"=? AND endpoint=?
  });

  $sth->execute($user, $endpoint);
  $sth->finish;
  $app->nocontent;
}

sub upsert {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $endpoint  = $req->parameters->{endpoint};
  my $auth      = $req->parameters->{auth};
  my $key       = $req->parameters->{key};

  if (my $error = check_subscription($endpoint, $key, $auth)) {
    return $app->error($error);
  }

  my $sth = $app->dbh->prepare(q{
    INSERT INTO web_push (endpoint, auth, key, "user")
      VALUES (?,?,?,?)
    ON CONFLICT ("user", endpoint)
      DO UPDATE SET auth=?, key=?
      WHERE web_push."user"=?
        AND web_push.endpoint=?
  });

  $sth->execute(
    $endpoint, $auth, $key, $user,
    $auth, $key, $user, $endpoint
  );

  $sth->finish;
  $app->nocontent;
}

1;
