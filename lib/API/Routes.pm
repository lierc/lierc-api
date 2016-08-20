package API::Routes;

use Util;
use Writer;

use List::Util qw(min);
use JSON::XS;
use URL::Encode qw(url_decode);

use Role::Tiny;

my %routes = map { $_ => 1} qw(
  user auth register logout
  list create show delete send
  logs logs_id
);

sub handle {
  my ($self, $name, $env, $captured, $session) = @_;
  if ($self->is_route($name)) {
    my $req = Plack::Request->new($env);
    return $self->$name($req, $captured, $session);
  }

  return $self->not_found;
}

sub is_route {
  my ($self, $name) = @_;
  return $name && exists $routes{$name};
}

sub create {
  my ($self, $req, $captures, $session) = @_;

  my $params = decode_json $req->content;
  for (qw(Host Port Nick)) {
    return $self->error("$_ is required")
      unless defined $params->{$_}
        && $params->{$_} =~ /\S/;
  }

  my $id  = Util->uuid;
  my $res = $self->request(POST => "$id/create", $req->content);

  if ($res->code == 200) {
    my $user = $session->{user};
    $self->dbh->do(
      q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)},
      {}, $id, $user, $req->content
    );

    return $self->json({success => "ok", "id" => $id});
  }

  $self->error($res->decoded_content);
}

sub delete {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->request(POST => "$id/destroy");

  if ($res->code == 200) {
    $self->dbh->do(q{DELETE FROM connection WHERE id=?}, {}, $id);
    return $self->ok;
  }

  $self->error($res->decoded_content);
}

sub send {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->request(POST => "$id/raw", $req->content);

  if ($res->code == 200) {
    return $self->ok;
  }

  $self->error($res->decoded_content);
}

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
  $self->error($res->decoded_content);
}

sub logs {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = $captures->{channel};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT id, message, connection FROM log
      WHERE channel=? AND connection=?
      ORDER BY id DESC LIMIT ?
    }, {}, url_decode($chan), $id, 100
  );

  my $json = JSON::XS->new;
  my $data = [
    map {
      {
        MessageId    => $_->[0],
        Message      => $json->decode($_->[1]),
        ConnectionId => $_->[2],
      }
    } @$rows
  ];
  return $self->json($data);
}

sub logs_id {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = $captures->{channel};
  my $event = $captures->{event};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT id, message, connection FROM log
      WHERE channel=? AND connection=? AND id < ?
      ORDER BY id DESC LIMIT ?
    }, {}, url_decode($chan), $id, $event, 100
  );

  my $json = JSON::XS->new;
  my $data = [
    map {
      {
        MessageId    => $_->[0],
        Message      => $json->decode($_->[1]),
        ConnectionId => $_->[2],
      }
    } @$rows
  ];
  return $self->json($data);
}

sub auth {
  my ($self, $req, $captures, $session) = @_;

  my $pass  = $req->parameters->{pass};
  my $email = $req->parameters->{email};
  my $hashed = Util->hash_password($pass, $self->secret);

  my ($row) = $self->dbh->selectall_array(
    q{SELECT id FROM "user" WHERE email=? AND password=?},
    {}, $email, $hashed
  );

  if ($row) {
    $req->env->{'psgix.session'}->{user} = $row->[0];
    return $self->ok;
  }

  return $self->unauthorized("Invalid email or password");
}

sub logout {
  my ($self, $req, $captures) = @_;
  delete $req->env->{'psgix.session'}->{user};
  return $self->ok;
}

sub register {
  my ($self, $req, $captures, $session) = @_;

  for (qw(email pass)) {
    die "$_ is required"
      unless defined $req->parameters->{$_}
        && $req->parameters->{$_} =~ /\S/;
  }

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};

  my ($id, $err) = $self->add_user($email, $pass);
  return $self->error($err) if $err;

  $session->{user} = $id;
  return $self->ok;
}

sub user {
  my ($self, $req, $captures, $session) = @_;
  return $self->json($session);
}

1;
