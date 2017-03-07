package API::Responses;

use Encode;
use JSON::XS;

use Role::Tiny;

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
  my ($self, @args) = @_;
  return $self->json({status => "ok", @args});
}

sub not_found {
  my $self = shift;
  $self->error("not found", 404);
}

sub error {
  my ($self, $error, $code) = @_;
  $error =~ s/ at [^\s]+ line \d+.*$//;
  if ($error =~ /^DBD::/) {
    ($error) = $error =~ /^DETAIL:\s*(.*)\s*$/m;
  }
  $error =~ s/\n+$//;
  my $data = { status => "error", error => $error };
  $self->json($data, $code || 400);
}

sub passthrough {
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
