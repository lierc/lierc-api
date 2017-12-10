package API::Controller::Connection;

use parent 'API::Controller';

use JSON::XS;

API->register("connection.list",   __PACKAGE__);
API->register("connection.create", __PACKAGE__);
API->register("connection.show",   __PACKAGE__);
API->register("connection.delete", __PACKAGE__);
API->register("connection.send",   __PACKAGE__);
API->register("connection.edit",   __PACKAGE__);

sub list {
  my ($app, $req) = @_;
  my $conns = $app->connections($req->session->{user});
  $app->json($conns);
}

sub show {
  my ($app, $req) = @_;

  my $id = $req->captures->{id};
  my $data = $app->lookup_config($id);
  my $config = decode_json $data;

  $app->json({ Id => $id, Config => $config });
}

sub create {
  my ($app, $req) = @_;

  my $params = decode_json $req->content;
  for (qw(Host Port Nick)) {
    die "$_ is required"
      unless defined $params->{$_}
        && $params->{$_} =~ /\S/;
  }

  my $id   = $req->captures->{id} || Util->uuid;
  my $user = $req->session->{user};

  $app->save_connection($id, $user, encode_json($params));
  $app->json({success => "ok", "id" => $id});
}

sub connect {
  my ($app, $req) = @_;

  my $id     = $req->captures->{id};
  my $config = $app->lookup_config($id);

  $app->db->do("UPDATE connection SET enabled=true");

  my $res = $app->request(POST => "$id/create", encode_json($config));
  die $res->decoded_content unless $res->code == 200;

  $app->json({success => "ok", "id" => $id});
}

sub disconnect {
  my ($app, $req) = @_;

  my $id     = $req->captures->{id};
  my $config = $app->lookup_config($id);

  $app->db->do("UPDATE connection SET enabled=false");

  my $res = $app->request(POST => "$id/destroy");
  die $res->decoded_content unless $res->code == 200;

  $app->json({success => "ok", "id" => $id});
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

  $app->run("connection.delete",     $req);
  $app->run("connection.create",  $req);

  $app->ok;
}

sub send {
  my ($app, $req) = @_;

  my $id    = $req->captures->{id};
  my $token = $req->headers->header('lierc-token');
  my $user  = $req->session->{user};

  unless ($app->check_token($user, $token)) {
    die "Invalid token";
  }

  my $res = $app->request(POST => "$id/raw", $req->content);

  if ($res->code == 200) {
    return $app->ok(token => $app->get_token($user));
  }

  die $res->decoded_content;
}

1;
