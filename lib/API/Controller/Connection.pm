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
API->register("connection.save",   __PACKAGE__);

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
  return valid_config_from($params);
}

# The settings for an existing connection: the request's, but with the
# saved channel list and nick. Both are only set when a connection is
# created; after that, joining and leaving channels and /nick change them
# (liercd reports the change and the logger saves it). The saved values are
# kept as they are, so an old one that today's rules refuse can't block an
# edit.
sub edited_config {
  my ($app, $req) = @_;
  my $params = eval { decode_json $req->content };
  die "Invalid connection settings\n" unless ref $params eq "HASH";
  my $saved = eval { decode_json($app->lookup_config($req->captures->{id})) };
  $saved = {} unless ref $saved eq "HASH";
  my $nick = $saved->{Nick};
  $nick = undef if ref $nick || !defined $nick || $nick eq "";

  delete $params->{Channels};
  # the request's nick is ignored; it's only checked if there's no saved
  # nick to keep
  $params->{Nick} = "placeholder" if defined $nick;
  my $config = valid_config_from($params);

  $config->{Nick} = $nick if defined $nick;
  $config->{Channels} = ref $saved->{Channels} eq "ARRAY" ? $saved->{Channels} : [];
  return $config;
}

sub valid_config_from {
  my $params = shift;
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
  my $config = edited_config($app, $req);
  my $id  = $req->captures->{id};
  my $res = $app->request(POST => "$id/destroy");
  die $res->decoded_content unless $res->code == 200 || $res->code == 404;
  $app->delete_connection($id);
  start($app, $id, $req->session->{user}, $config);

  return $app->ok;
}

# Saves a connection's highlight words and network alias, which don't need
# a reconnect: the notifier reloads highlights, and the UI shows the alias.
# Everything else needs Save & Reconnect (edit).
sub save {
  my ($app, $req) = @_;

  my $params = eval { decode_json $req->content };
  die "Invalid connection settings\n" unless ref $params eq "HASH";
  my $names = API::ConnectionConfig->clean_names($params);

  # the rest of the saved config is kept as it is: it's what liercd is
  # running, and an old value or a DNS failure can't block this
  my $config = eval { decode_json($app->lookup_config($req->captures->{id})) };
  die "Invalid connection settings\n" unless ref $config eq "HASH";
  delete $config->{Alias};
  %$config = (%$config, %$names);

  $app->update_connection($req->captures->{id}, encode_json $config);
  $app->dbh->do("NOTIFY highlights");

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
