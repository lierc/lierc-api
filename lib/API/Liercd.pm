package API::Liercd;

use LWP::UserAgent;
use Role::Tiny;

sub ua {
  my $self = shift;
  $self->{ua} ||= LWP::UserAgent->new;
}

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
