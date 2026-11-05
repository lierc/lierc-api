package API::Controller::Connection;

use parent 'API::Controller';

use Util;
use API::ConnectionConfig;
use API::Limits;
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
  my $user = $req->session->{user};
  my $max = API::Limits::limit($app, $user, "connections");
  start($app, $id, $user, $config, $max);
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
# saved channel list. Channels are only set when a connection is created;
# after that, joining and leaving channels change them (liercd reports the
# change and the logger saves it). The nick is the request's, checked like
# the rest, so a nick the server refuses can be fixed here; a request
# without one (an older UI's) keeps the saved nick, which /nick also
# changes. A kept nick isn't re-checked, so an old one that today's rules
# refuse can't block an edit.
sub edited_config {
  my ($app, $req) = @_;
  my $params = eval { decode_json $req->content };
  die "Invalid connection settings\n" unless ref $params eq "HASH";
  my $saved = eval { decode_json($app->lookup_config($req->captures->{id})) };
  $saved = {} unless ref $saved eq "HASH";
  my $nick = $saved->{Nick};
  $nick = undef if ref $nick || !defined $nick || $nick eq "";

  delete $params->{Channels};
  my $keep_nick = defined $nick && (!defined $params->{Nick} || $params->{Nick} eq "");
  # a kept nick is only checked if there's no saved one
  $params->{Nick} = "placeholder" if $keep_nick;
  my $config = valid_config_from($params);

  $config->{Nick} = $nick if $keep_nick;
  $config->{Channels} = ref $saved->{Channels} eq "ARRAY" ? $saved->{Channels} : [];
  return $config;
}

# The settings liercd connects with, as one string to compare: a saved
# config may have a string port, 0 or 1 for a flag, or "" for no username.
# The nick is one: liercd registers with it, so changing it reconnects.
my @SERVER_FIELDS = qw(Host Port Nick User Pass Ssl SASL);
sub server_key {
  my $config = shift;
  my %c = %$config;
  $c{Host} = lc($c{Host} // "") =~ s/^\s+|\s+$//gr;
  $c{Port} = ($c{Port} // "") =~ /^\s*(\d+)\s*$/ ? int $1 : $c{Port} // "";
  $c{$_} = $c{$_} // "" for qw(Nick User Pass);
  $c{$_} = $c{$_} ? 1 : 0 for qw(Ssl SASL);
  return join "\0", map { ref $c{$_} ? "" : $c{$_} } @SERVER_FIELDS;
}

sub valid_config_from {
  my $params = shift;
  my $config = API::ConnectionConfig->clean($params);
  API::ConnectionConfig->check_host($config->{Host}, $config->{Port});
  return $config;
}

# Saves the row before liercd creates the connection: which liercd it
# goes to is looked up from the row (the column's default can send new
# connections to liercd-next), and the stream sends the connection's
# first events to whoever the row says owns it. If the create fails,
# the row is deleted again. A user who already has $max connections gets
# no new one, and liercd isn't asked.
sub start {
  my ($app, $id, $user, $config, $max) = @_;
  my $json = encode_json $config;
  $app->save_connection_limited($id, $user, $json, $max)
    or die "You can have at most $max connections\n";

  my $res = eval { $app->request(POST => "$id/create", $json) };
  my $err = $@ || ($res->code == 200 ? undef : $res->decoded_content);
  if (defined $err) {
    $app->delete_connection($id);
    die $err;
  }

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
  my $saved = eval { decode_json($app->lookup_config($id)) };

  # nothing the IRC connection uses changed (the alias and highlights
  # don't need it), so just save
  if (ref $saved eq "HASH" && server_key($saved) eq server_key($config)) {
    $app->update_connection($id, encode_json $config);
    $app->dbh->do("NOTIFY highlights");
    return $app->ok;
  }

  my $res = $app->request(POST => "$id/destroy");
  die $res->decoded_content unless $res->code == 200 || $res->code == 404;

  # the row stays put: the stream server sends a connection's events to
  # whoever its row says owns it, so while it was deleted and reinserted
  # the CREATE event could find no owner and be dropped
  my $json = encode_json $config;
  $app->update_connection($id, $json);
  $res = $app->request(POST => "$id/create", $json);
  die $res->decoded_content unless $res->code == 200;
  $app->dbh->do("NOTIFY highlights");

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
