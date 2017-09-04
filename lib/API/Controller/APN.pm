package API::Controller::APN;

use parent 'API::Controller';

use File::Temp qw(tempdir);
use Capture::Tiny qw(capture);
use Digest::SHA qw(sha1_hex);
use File::Copy;
use JSON::XS;

API->register("apn.package", __PACKAGE__);

sub package {
  my ($app, $req) = @_;
  my $config = $app->apn;
  my $user = $req->session->{user};

  my $dir = tempdir( CLEANUP => 1 );
  my %manifest;

  my $website = encode_json {
    websiteName         => $config->{website_name},
    websitePushID       => $config->{website_pushid},
    allowedDomains      => $config->{allowed_domains},
    urlFormatString     => $config->{format_string},
    authenticationToken => $user,
    webServiceURL       => $config->{push_url},
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
      "-outform", "pem",
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
