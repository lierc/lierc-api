package API::Controller::APN;

use parent 'API::Controller';

use File::Temp qw(tempdir);
use Capture::Tiny qw(capture);
use Digest::SHA qw(sha1_hex);
use File::Copy;
use JSON::XS;

API->register("apn.package",    __PACKAGE__);
API->register("apn.log",        __PACKAGE__);
API->register("apn.register",   __PACKAGE__);
API->register("apn.device",     __PACKAGE__);
API->register("apn.unregister", __PACKAGE__);
API->register("apn.config",     __PACKAGE__);

sub unregister {
  my ($app, $req) = @_;
  my $device_id = $req->captures->{device_id};
  my $push_id   = $req->captures->{push_id};

  my ($auth, $user) = split " ", $req->header('authorization');
  die "Invalid authorization header"
    unless $auth eq 'ApplePushNotifications';

  my $sth = $app->dbh->prepare_cached(q{
    DELETE FROM apn
    WHERE "user"=? AND device_id=?
  });

  $sth->execute($user, $device_id);
  $app->nocontent;
}

sub device {
  my ($app, $req) = @_;
  my $device_id = $req->content;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    INSERT INTO apn (device_id, "user")
      VALUES(?,?)
    ON CONFLICT ("user", device_id)
      DO UPDATE SET updated=NOW()
  });

  $sth->execute($device_id, $user);
  $sth->finish;
  $app->ok;
}

sub register {
  my ($app, $req) = @_;
  my $device_id = $req->captures->{device_id};
  my $push_id   = $req->captures->{push_id};

  my ($auth, $user) = split " ", $req->header('authorization');
  die "Invalid authorization header"
    unless $auth eq 'ApplePushNotifications';

  my $sth = $app->dbh->prepare_cached(q{
    INSERT INTO apn (device_id, "user")
      VALUES(?,?)
    ON CONFLICT ("user", device_id)
      DO UPDATE SET updated=NOW()
  });

  $sth->execute($device_id, $user);
  $sth->finish;
  $app->ok;
}

sub log {
  my ($app, $req) = @_;
  warn $req->content;
  $app->nocontent;
}

sub config {
  my ($app, $req) = @_;
  my $config = $app->apn;
  my $user = $req->session->{user};
  my $config = {
    push_id     => $config->{website_pushid},
    service_url => $config->{service_url},
    user        => $user,
  };
  $app->json($config);
}

sub package {
  my ($app, $req) = @_;
  my $config = $app->apn;

  my $data = decode_json $req->content;
  my $dir = tempdir( CLEANUP => 1 );
  my %manifest;

  my $website = encode_json {
    websiteName         => $config->{website_name},
    websitePushID       => $config->{website_pushid},
    allowedDomains      => $config->{allowed_domains},
    urlFormatString     => $config->{format_string},
    authenticationToken => $data->{user},
    webServiceURL       => $config->{service_url},
  };

  $manifest{"website.json"} = sha1_hex($website);

  {
    open my $fh, '>', "$dir/website.json"
      or die "Unable to write website.json: $!";
    print $fh $website;
  }

  mkdir "$dir/icon.iconset"
    or die "Unable to create icon.iconset directory: $!";

  my @icons = qw(16x16 16x16@2x 32x32 32x32@2x 128x128 128x128@2x);
  for (@icons) {
    my $file = "icon.iconset/icon_$_.png";
    copy($file, "$dir/$file")
      or die "Unable to copy icon: $!";
    $manifest{$file} = Digest::SHA->new->addfile("$dir/$file")->hexdigest;
  }

  {
    open my $fh, ">", "$dir/manifest.json"
      or die "Unable to open manifest.json: $!";
    print $fh encode_json \%manifest;
  }

  my (undef, $err, $exit) = capture {
    system(
      "openssl", "smime", "-sign",
      "-in", "$dir/manifest.json",
      "-out", "$dir/signature",
      "-outform", "der",
      "-inkey", $config->{key_file},
      "-signer", $config->{cert_file},
    );
  };

  if ($exit != 0) {
    die "Failed to generate signature: $err";
  }

  ($out, $err, $exit) = capture {
    system("cd $dir && zip -r - .");
  };

  if ($exit != 0) {
    die "Failed to zip $dir: $err";
  }

  return [
    200,
    [
      "Content-Type" => "application/zip",
      "Content-Lenght" => length($out),
    ],
    [$out]
  ];
}

1;
