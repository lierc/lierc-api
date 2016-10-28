package API::Routes;

use Util;
use Writer;

use List::Util qw(min);
use JSON::XS;
use Data::Validate::Email;
use Encode;

use Role::Tiny;

my %routes = map { $_ => 1} qw(
  user auth register logout
  list create show delete send edit
  logs logs_id pref prefs set_pref
  unread privates
);

sub handle {
  my ($self, $name, $env, $captured, $session) = @_;
  if ($self->is_route($name)) {
    my $req = Plack::Request->new($env);

    my ($res, $err);
    {
      local $@;
      $res = eval { $self->$name($req, $captured, $session) };
      $err = $@;
    }

    return $self->error($err) if $err;
    return $res;
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

sub logs {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = decode utf8 => $captures->{channel};
  my $logs = $self->find_logs($chan, $id);

  my $json = JSON::XS->new;
  my $data = [
    map {
      {
        MessageId    => $_->[0],
        Message      => $json->decode($_->[1]),
        ConnectionId => $_->[2],
      }
    } @$logs
  ];
  return $self->json($data);
}

sub logs_id {
  my ($self, $req, $captures, $session) = @_;
  my $id   = $captures->{id};
  my $chan = decode utf8 => $captures->{channel};
  my $event = $captures->{event};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT id, message, connection FROM log
      WHERE channel=? AND connection=? AND id < ?
      ORDER BY id DESC LIMIT ?
    }, {}, $chan, $id, $event, 50
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

  die "Invalid email address"
    unless Data::Validate::Email::is_email($email);

  my $id = $self->add_user($email, $pass);
  $session->{user} = $id;
  return $self->ok;
}

sub user {
  my ($self, $req, $captures, $session) = @_;
  my $user = $self->lookup_user($session->{user});
  return $self->json({
    email => $user->{email},
    user => $user->{id},
  });
}

sub prefs {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT name, value FROM pref
    WHERE "user"=?
  }, {Slice => {}}, $user);

  return $self->not_found unless $rows;
  return $self->json($rows);
}

sub pref {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  my $row = $self->dbh->selectrow_hashref(q{
    SELECT name, value FROM pref
    WHERE "user"=? AND name=?
  }, {}, $user, $pref);

  return $self->not_found unless $row;
  return $self->json($row);
}

sub set_pref {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $pref = $captures->{pref};

  $self->dbh->do(q{
    INSERT INTO pref ("user",name,value) VALUES(?,?,?)
    ON CONFLICT ("user", name)
    DO UPDATE SET value=? WHERE pref.user=? AND pref.name=?
    }, {},
    $user, $pref, $req->content,
    $req->content, $user, $pref
  );

  $self->ok;
}

sub unread {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};
  my $last = $captures->{event};

  my $sth = $self->dbh->prepare(q{
    SELECT COUNT(*), channel, privmsg, connection
    FROM log
    JOIN connection
      ON connection.id=log.connection
    JOIN "user"
      ON "user".id=connection.user
    WHERE
      "user".id=?
    AND log.id > ?
    GROUP BY channel, connection, privmsg
  });

  my %channels;

  $sth->execute($user, $last);

  while (my $row = $sth->fetchrow_hashref) {
    my $key = $row->{privmsg} ? "messages" : "events";
    my $conn = $row->{connection};
    my $chan = $row->{channel};
    $channels{ $conn }{ $chan }{ $key } += $row->{count};
  }

  $self->json(\%channels);
}

sub privates {
  my ($self, $req, $captures, $session) = @_;
  my $user = $session->{user};

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT DISTINCT(log.channel) as nick, log.connection
    FROM log
    JOIN connection
      ON log.connection=connection.id
    JOIN "user"
      ON connection.user="user".id
    WHERE log.privmsg = TRUE
    AND connection.user=?
    AND (
      log.time > NOW() - INTERVAL '1 day'
      OR log.id > "user".last_id
    )
  }, {Slice => {}}, $user);

  $self->json([ grep { $_->{nick} =~ /^[^#&+!]/ } @$rows ]);
}

1;
