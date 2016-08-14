package Response;

use Encode;
use JSON::XS;
use Exporter qw(import);

our @EXPORT = qw(
  html ok nocontent error pass
  unauthorized not_found json event_stream
);

sub unauthorized {
  my ($self, $msg) = @_;
  $self->error($msg || "unauthorized", 401);
}

sub json {
  my ($self, $data, $code) = @_;
  return [
    $code || 200,
    ["Content-Type", "application/javascript;charset=utf-8"],
    [encode_json $data]];
}

sub nocontent {
  return [204, [], []];
}

sub ok {
  my $self = shift;
  return $self->json({status => "ok"});
}


sub html {
  my ($self, $html) = @_;
  return [
    200,
    ["Content-Type", "text/html;charset=utf-8"],
    $html
  ];
};

sub not_found {
  my $self = shift;
  $self->error("not found", 404);
}

sub error {
  my ($self, $error, $code) = @_;
  my $data = { status => "error", error => $error };
  $self->json($data, $code || 400);
}

sub pass {
  my ($self, $res) = @_;
  return [
    $res->code,
    [$res->flatten],
    [$res->content],
  ];
}

sub event_stream {
  return [
    200,
    ["Content-Type", "text/event-stream;charset=utf-8"]
  ];
}

1;
