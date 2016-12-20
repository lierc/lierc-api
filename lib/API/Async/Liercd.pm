package API::Async::Liercd;

use HTTP::Response;
use AnyEvent::HTTP qw();
use Role::Tiny;

sub url {
  my ($self, $path) = @_;
  sprintf "http://%s:5005/%s", $self->host, $path;
}

sub request {
  my ($self, $meth, $path, $content, $type) = @_;
  my $cv = AE::cv;

  $type = "application/javascript" unless defined $type;

  AnyEvent::HTTP::http_request $meth, $self->url($path),
    persistent => 0,
    keepalive => 0,
    headers => { "Content-Type" => $type },
    $content ? (body => $content) : (),
    sub {
      my ($body, $h) = @_;
      my $res = HTTP::Response->new(
        $h->{Status}, $h->{Reason}, 
        [ map { $_ => $h->{$_} } grep /^a-z/, keys %$h ],
        $body
      );
      $cv->send($res);
    };

  return $cv;
}

sub find_or_recreate_connection {
  my ($self, $id, $user) = @_;
  my $done = AE::cv;

  my $cv = $self->lookup_config($id);

  $cv->cb(sub {
    my $config = $_[0]->recv;
    return $done->croak("Connection does not exist")
      unless defined $config;

    my $cv = $self->request(GET => "$id/status");

    $cv->cb(sub {
      my $res = $_[0]->recv;
      return $done->send($res) if $res->code == 200;

      my $cv = $self->request(POST => "$id/create", $config);

      $cv->cb(sub {
        my $res = $_[0]->recv;
        return $done->croak("Unable to create new connection: " . $res->status_line)
          unless $res->code == 200;

        my $cv = $self->request(GET => "$id/status");

        $cv->cb(sub {
          my $res = $_[0]->recv;
          return $done->croak( "Unable to create new connection: " . $res->status_line)
            unless $res->code == 200;

          $done->send($res);
        });
      });
    });
  });

  $done;
}

1;
