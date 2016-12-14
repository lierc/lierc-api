package API::Controller::Connection;

use parent 'API::Controller';

use JSON::XS;

API->register("connection.list",   [__PACKAGE__, "list"]);
API->register("connection.create", [__PACKAGE__, "create"]);
API->register("connection.show",   [__PACKAGE__, "show"]);
API->register("connection.delete", [__PACKAGE__, "delete"]);
API->register("connection.send",   [__PACKAGE__, "send"]);
API->register("connection.edit",   [__PACKAGE__, "edit"]);

sub list {
  my ($app, $req) = @_;
  my $conns = $app->connections($req->session->{user});

  return $app->json($conns);
}

sub show {
  my ($app, $req) = @_;

  my $id = $req->captures->{id};
  my $res = $app->request(GET => "$id/status");

  return $app->pass($res) if $res->code == 200;
  die $res->decoded_content;
}

sub create {
  my ($app, $req) = @_;

  my $params = decode_json $req->content;
  for (qw(Host Port Nick)) {
    die "$_ is required"
      unless defined $params->{$_}
        && $params->{$_} =~ /\S/;
  }

  my $id  = $req->captures->{id} || Util->uuid;
  my $res = $app->request(POST => "$id/create", $req->content);

  if ($res->code == 200) {
    my $user = $req->session->{user};
    $app->save_connection($id, $user, $req->content);
    return $app->json({success => "ok", "id" => $id});
  }

  die $res->decoded_content;
}

sub delete {
  my ($app, $req) = @_;

  my $id  = $req->captures->{id};
  my $res = $app->request(POST => "$id/destroy");

  if ($res->code == 200) {
    $app->delete_connection($id);
    return $app->ok;
  }

  die $res->decoded_content;
}

sub edit {
  my ($app, $req) = @_;

  $app->handle("connection.delete", $req);
  $app->handle("connection.create", $req);

  return $app->ok;
}

sub send {
  my ($app, $req) = @_;

  my $id  = $req->captures->{id};
  my $res = $app->request(POST => "$id/raw", $req->content);

  if ($res->code == 200) {
    return $app->ok;
  }

  die $res->decoded_content;
}

1;
