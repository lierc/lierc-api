package Client;

use LWP::UserAgent;

use Class::Tiny qw(host), {
  ua => sub { LWP::UserAgent->new }
};

sub url {
  my ($self, $path) = @_;
  sprintf "http://%s/%s", $self->host, $path;
}

sub request {
  my ($self, $meth, $path, $content, $type) = @_;
  $type = "application/javascript" unless defined $type;
  my $req = HTTP::Request->new($meth, $self->url($path), [], $content);
  my $res = $self->ua->request( $req );
  return $res;
}

1;
