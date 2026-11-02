package API::Controller::Connection;

use parent 'API::Controller';

use Util;
use API::ConnectionConfig;
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

  return $app->json($conns);
}

sub show {
  my ($app, $req) = @_;

  my $id = $req->captures->{id};
  my $data = $app->lookup_config($id);
  my $config = decode_json $data;

  return $app->json({ Id => $id, Config => $config });
}

sub create {
  my ($app, $req) = @_;

  my $config = valid_config($req);
  my $id = Util->uuid;
  start($app, $id, $req->session->{user}, $config);
  return $app->json({success => "ok", "id" => $id});
}

# The request's connection settings, cleaned; dies with a message for the
# user if they're invalid or the host is private.
sub valid_config {
  my $req = shift;
  my $params = eval { decode_json $req->content };
  die "Invalid connection settings\n" unless ref $params eq "HASH";
  my $config = API::ConnectionConfig->clean($params);
  API::ConnectionConfig->check_host($config->{Host}, $config->{Port});
  return $config;
}

sub start {
  my ($app, $id, $user, $config) = @_;
  my $json = encode_json $config;
  my $res  = $app->request(POST => "$id/create", $json);
  die $res->decoded_content unless $res->code == 200;
  $app->save_connection($id, $user, $json);
  $app->dbh->do("NOTIFY highlights");
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

  # check everything before touching the running connection, so a rejected
  # edit leaves it as it was
  my $config = valid_config($req);
  my $id  = $req->captures->{id};
  my $res = $app->request(POST => "$id/destroy");
  die $res->decoded_content unless $res->code == 200 || $res->code == 404;
  $app->delete_connection($id);
  start($app, $id, $req->session->{user}, $config);

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
