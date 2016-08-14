package App;

use strict;
use warnings;

use Text::Xslate;
use URL::Encode qw(url_decode);
use JSON::XS;
use AnyEvent;
use List::Util qw(min);
use DBIx::Connector;

use Util;
use Response;
use Writer;
use Client;

use Class::Tiny qw(host dsn dbuser dbpass secret base), {
  streams => sub { {} },
  client  => sub { Client->new(host => $_[0]->host) },
};

sub BUILD {
  my $self = shift;

  # send a ping to every stream every 10s
  $self->{ping} = AE::timer 0, 30, sub {
    for my $writers (values %{$self->streams}) {
      for my $writer (values %$writers) {
        $writer->ping;
      }
    }
  };
}

sub dbh {
  my $self = shift;
  ($self->{dbh} ||= DBIx::Connector->new(
    $self->dsn, $self->dbuser, $self->dbpass,
    {
      RaiseError => 1,
      AutoCommit => 1,
    }
  ))->dbh;
}

sub template {
  my $self = shift;
  $self->{template} ||= Text::Xslate->new(
    path => "./templates",
    function => {
      asset => sub { $self->path("/static/$_[0]") },
    }
  );
}

{
  my %handlers = map { $_ => 1} qw(
    user auth register list create show
    delete send events slice login
  );

  sub handle {
    my ($self, $name, $env, $captured, $session) = @_;
    if (my $handler = $handlers{$name}) {
      my $req = Plack::Request->new($env);
      return $self->$name($req, $captured, $session);
    }

    return $self->not_found;
  }
}

sub login {
  my $self = shift;
  $self->html("login");
}

sub events {
  my ($self, $req, $captures, $session) = @_;
  my $id = $captures->{id};
  
  return sub {
    my $response = shift;
    my $handle = $response->([200, ["Content-Type", "text/event-stream"]]);
    my $writer = $self->add_writer($id, $handle);
    $self->push_fake_events($id, $writer);
  };
}

sub add_writer {
  my ($self, $id, $handle) = @_;

  my $writer = Writer->new(
    handle => $handle,
    on_close => sub {
      my $w = shift;
      delete $self->streams->{$id}->{$w->id};
    }
  );

  $self->streams->{$id}->{$writer->id} = $writer;

  return $writer;
}

sub push_fake_events {
  my ($self, $id, $writer) = @_;

  my $res = $self->find_or_recreate_connection($id);
  my $status = decode_json($res->content);
  my $welcome = "Welcome to the Internet Relay Network $status->{Nick}";

  $writer->irc_event(lies => "001", $status->{Nick}, $welcome);

  for my $name (keys %{ $status->{Channels} }) {
    my $channel = $status->{Channels}{$name};
    $writer->irc_event($status->{Nick} => "JOIN", $name);

    if ($channel->{Topic}{Topic}) {
      $writer->irc_event(
        lies => 332,
        $status->{Nick}, $channel->{Name}, $channel->{Topic}{Topic}
      );
    }

    if ($channel->{Nicks}) {
      my $nicks = join " ", keys %{ $channel->{Nicks} };
      $writer->irc_event(
        lies => "353",
        $status->{Nick}, "=", $channel->{Name}, $nicks
      );
      $writer->irc_event(
        lies => "366",
        $status->{Nick}, $channel->{Name}, "End of /NAMES list."
      );
    }
  }
}

sub irc_event {
  my ($self, $msg) = @_;

  my $data = decode_json($msg);
  my $id = $data->{Id};

  if (my $streams = $self->streams->{$id}) {
    my $msg_id = $data->{Message}->{Id};
    my $event = Util->event(irc => encode_json($data->{Message}), $msg_id);
    $_->write($event) for values %$streams;
  }
}

sub request {
  my $self = shift;
  $self->client->request(@_);
}

sub create {
  my ($self, $req, $captures, $session) = @_;

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

  $self->error($res->decoded_content);
}

sub list {
  my ($self, $req, $captures, $session) = @_;
  my $conns = $self->connections($session->{user});

  return $self->json($conns);
}

sub find_or_recreate_connection {
  my ($self, $id, $user) = @_;

  my ($row) = $self->dbh->selectall_array(
    q{SELECT config FROM connection WHERE id=?},
    {}, $id
  );

  die "Connection does not exist"
    unless $row;

  my $res = $self->request(GET => "$id/status");
  return $res if $res->code == 200;

  $res = $self->request(POST => "$id/create", $row->[0]);

  die "Unable to create new connection: " . $res->status_line
    unless $res->code == 200;

  $res = $self->request(GET => "$id/status");
  return $res;
}

sub show {
  my ($self, $req, $captures, $session) = @_;

  my $id = $captures->{id};
  my $res = $self->request(GET => "$id/status");

  return $self->pass($res) if $res->code == 200;
  $self->error($res->decoded_content);
}

sub slice {
  my ($self, $req, $captures, $session) = @_;
  my $id    = $captures->{id};
  my $chan  = $captures->{channel};
  my $slice = $captures->{slice};

  my ($start, $end);

  if ( $slice =~ /^(\d+):(\d+)$/) {
    $start = $1;
    $end   = $2;
  }
  elsif ($slice =~ /^:(\d+)$/) {
    $start = 0;
    $end   = $1;
  }
  elsif ($slice =~ /^(\d+):$/) {
    $start = $1;
    $end   = $start + 100; 
  }

  $end = min($end, $start + 100);

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT message FROM log
      WHERE channel IN (?, '*') AND connection=?
      ORDER BY time DESC OFFSET ? LIMIT ?
    }, {}, url_decode($chan), $id, $start, $end - $start
  );

  return $self->not_found unless @$rows;

  my $data = [ map { decode_json $_->[0] } @$rows ];
  return $self->json($data);
}

sub logged_in {
  my ($self, $session) = @_;

  return () unless defined $session && defined $session->{user};
  return () unless $self->lookup_user($session->{user});
  return 1;
}

sub user {
  my ($self, $req, $captures, $session) = @_;
  return $self->json($session);
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

sub add_user {
  my ($self, $email, $pass) = @_;
  my $hashed = Util->hash_password($pass, $self->secret);
  my $id = Util->uuid;

  my $err;

  {
    local $@;
    eval {
      $self->dbh->do(
        q{INSERT INTO "user" (id, email, password) VALUES(?,?,?)},
        {}, $id, $email, $hashed
      );
    };
    $err = $@;
  }

  return ($id, $err);
}

sub lookup_user {
  my ($self, $id) = @_;
  return () unless defined $id;

  my ($user) = $self->dbh->selectall_array(
    q{SELECT * FROM "user" WHERE id=?},
    {}, $id
  );

  return $user;
}

sub register {
  my ($self, $req, $captures, $session) = @_;

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};

  if (defined $email and defined $pass) {
    my ($id, $err) = $self->add_user($email, $pass);
    return $self->error($err) if $err;

    $session->{user} = $id;
    return $self->ok;
  }

  return $self->error("Email and password required");
}

sub connections {
  my ($self, $user) = @_;
  my $rows = $self->dbh->selectall_arrayref(
    q{SELECT id, config FROM connection WHERE "user"=?},
    {}, $user
  );
  return [ map {
    { id => $_->[0], Config => decode_json($_->[1]) }
  } @$rows ];
}

sub path {
  my ($self, $path) = @_;
  my $redir = $self->base . "/" . $path;
  $redir =~ s{//}{/}g;
  $redir = "/" if $redir eq "";
  return $redir;
}

sub verify_owner {
  my ($self, $id, $user) = @_;

  my $rows = $self->dbh->selectall_arrayref(
    q{SELECT id FROM connection WHERE "user"=? AND id=?},
    {}, $user, $id
  );

  return @$rows > 0;
}

1;
