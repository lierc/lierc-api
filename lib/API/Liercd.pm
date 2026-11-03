package API::Liercd;

use LWP::UserAgent;
use Role::Tiny;

sub ua {
  my $self = shift;
  $self->{ua} ||= LWP::UserAgent->new;
}

sub liercd_url {
  my ($self, $path) = @_;

  # The connection id is the first path segment; its row says which
  # liercd instance it lives on (NULL or '' is main, as in Go).
  my ($id) = $path =~ m{^/*([^/]*)};
  my $instance = length $id ? $self->lookup_liercd($id) : undef;

  my $host;
  if (!defined $instance || $instance eq "") {
    $host = $self->host;
  }
  elsif ($instance eq "next") {
    $host = $self->next_host;
    die qq{liercd instance "next" has no host (LIERCD_NEXT_HOST)\n}
      unless defined $host && length $host;
  }
  else {
    die qq{unknown liercd instance "$instance"\n};
  }

  # liercd's port is 5005 unless the host gives its own ("127.0.0.1:5006"),
  # as Go's baseURL does
  $host .= ":5005" unless $host =~ /:\d+\z/;
  sprintf "http://%s/%s", $host, $path;
}

sub request {
  my ($self, $meth, $path, $content, $type) = @_;
  $type = "application/javascript" unless defined $type;
  my $req = HTTP::Request->new($meth, $self->liercd_url($path), [], $content);
  my $res = $self->ua->request( $req );
  return $res;
}

sub find_or_recreate_connection {
  my ($self, $id, $user) = @_;

  my $config = $self->lookup_config($id);

  die "Connection does not exist"
    unless defined $config;

  my $res = $self->request(GET => "$id/status");
  return $res if $res->code == 200;

  $res = $self->request(POST => "$id/create", $config);

  die "Unable to create new connection: " . $res->status_line
    unless $res->code == 200;

  $res = $self->request(GET => "$id/status");

  die "Unable to create new connection: " . $res->status_line
    unless $res->code == 200;

  return $res;
}

1;
