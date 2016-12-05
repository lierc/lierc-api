package API::Controller::Connection;

use parent 'API::Controller';

API->register("connection.list",   [__PACKAGE__, "list"]);
API->register("connection.create", [__PACKAGE__, "create"]);
API->register("connection.show",   [__PACKAGE__, "show"]);
API->register("connection.delete", [__PACKAGE__, "delete"]);
API->register("connection.send",   [__PACKAGE__, "send"]);
API->register("connection.edit",   [__PACKAGE__, "edit"]);

sub list {
  my ($self, $req, $captures, $session) = @_;
  my $conns = $self->connections($session->{user});

  return $self->json($conns);
}

sub show {
  my ($self, $req, $captures, $session) = @_;

  my $id = $captures->{id};
  my $res = $self->request(GET => "$id/status");

  return $self->pass($res) if $res->code == 200;
  die $res->decoded_content;
}

sub create {
  my ($self, $req, $captures, $session) = @_;

  my $params = decode_json $req->content;
  for (qw(Host Port Nick)) {
    die "$_ is required"
      unless defined $params->{$_}
        && $params->{$_} =~ /\S/;
  }

  my $id  = $captures->{id} || Util->uuid;
  my $res = $self->request(POST => "$id/create", $req->content);

  if ($res->code == 200) {
    my $user = $session->{user};
    $self->save_connection($id, $user, $req->content);
    return $self->json({success => "ok", "id" => $id});
  }

  die $res->decoded_content;
}

sub delete {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->request(POST => "$id/destroy");

  if ($res->code == 200) {
    $self->delete_connection($id);
    return $self->ok;
  }

  die $res->decoded_content;
}

sub edit {
  my ($self, $req, $captures, $session) = @_;

  $self->delete($req, $captures, $session);
  $self->create($req, $captures, $session);

  return $self->ok;
}

sub send {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->request(POST => "$id/raw", $req->content);

  if ($res->code == 200) {
    return $self->ok;
  }

  die $res->decoded_content;
}

1;
