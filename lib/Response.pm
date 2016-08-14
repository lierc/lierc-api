package Response;

use Encode;
use JSON::XS;
use Exporter qw(import);

our @EXPORT = qw(
  html ok nocontent redirect
  unauthorized not_found json text
);

sub redirect {
  my ($self, $path) = @_;
  return [
    302,
    ["Location", $self->path($path)],
    ["go there"]];
}

sub unauthorized {
  return [
    401,
    ["Content-Type", "application/javascript"],
    [encode_json {"status" => "unauthorized"}]];
}

sub json {
  my ($self, $data) = @_;
  return [
    200,
    ["Content-Type", "application/javascript"],
    [encode_json $data],];
}

sub text {
  my ($self, $text) = @_;
  return [
    200,
    ["Content-Type", "text/plain"],
    [$text]];
}

sub nocontent {
  return [204, [], []];
}

sub ok {
  my $self = shift;
  return $self->json({status => "ok"});
}


sub html {
  my ($self, $template, $vars) = @_;
  my $html = $self->template->render("$template.html", $vars);
  return [
    200,
    ["Content-Type", "text/html;charset=utf-8"],
    [encode utf8 => $html]];
};

sub not_found {
  return [
    404,
    ["Content-Type", "application/javascript"],
    [encode_json {"status" => "not found"}]];
}

1;
