package API::Controller::Image;

use parent 'API::Controller';

use IO::Socket::SSL;
use JSON::XS;

API->register("image.create", __PACKAGE__);

sub create {
  my ($app, $req) = @_;
  my $key = $app->imgur_key;
  my $fh = $req->input;

  my $s = IO::Socket::SSL->new('api.imgur.com:443')
    or die "Unable to connect to api.imgur.com: $!, $SSL_ERROR";

  binmode $s;

  print $s "POST /3/image HTTP/1.0\n";
  print $s "Host: api.imgur.com\n";
  print $s "Authorization: Client-ID $key\n";
  print $s "Transfer-Encoding: chunked\n";
  
  for (qw{Content-Length Content-Type}) {
    my $v = $req->header($_);
    print $s "$_: $v\n" if defined $v;
  }

  print $s "\n";

  my $buf;
  warn "reading from $fh";
  while ((my $bytes = read($s, $fh, 1024)) > 0) {
    warn "read $bytes";
    print $s sprintf("%X", $bytes);
    print $s "\r\n";
    print $s $chunk;
    print $s "\r\n";
  }

  print $s "0\r\n";
  print $s "\r\n";

  my @lines = $s->getlines;
  my $res = decode_json join "", @lines;

  close $fh;
  close $s;

  $app->json($res);
}

1;
