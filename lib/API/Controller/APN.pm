package API::Controller::APN;

use parent 'API::Controller';

use File::Temp qw(tempdir);
use Capture::Tiny;
use File::Copy;
use JSON::XS;

API->register("apn.pushpackage", __PACKAGE__);

sub pushpackage {
  my ($app, $req) = @_;
  my $config = $app->apn;

  my $dir = tempdir( CLEANUP => 1 );
  my %manifest;

  my $website = encode_json {
    websiteName         => $config->{website_name},
    websitePushID       => $config->{website_pushid},
    allowedDomains      => $config->{allowed_domains},
    urlFormatString     => $config->{format_string},
    authenticationToken => $config->{auth_token},
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
    $manifest{$file} = Digest::SHA->new->addfile("$dir/$file")->hex_digest;
  }

  {
    open my $fh, ">", "$dir/manifest.json"
      or die "Unable to open manifest.json: $!";
    print $fh encode_json \$manifest;
  }

  opendir my $dh, $dir
    or die "Cannot read dir: $dir";

  my ($out, $err, $exit) = capture {
    system("zip", "-r", "-", "$dir/*");
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
