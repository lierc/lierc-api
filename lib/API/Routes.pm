package API::Routes;

use Util;
use Writer;

use List::Util qw(min);
use JSON::XS;
use URL::Encode qw(url_decode);

use Role::Tiny;

my %routes = map { $_ => 1} qw(
  user auth register list create show
  delete send events slice login
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
  return $routes{$name};
}

sub events {
  my ($self, $req, $captures, $session) = @_;

  my $user = $session->{user};
  my $conns = $self->connections($user);
  
  return sub {
    my $response = shift;

    my $handle = $response->($self->event_stream);
    my $writer = Writer->new(
      handle => $handle,
      on_close => sub {
        my $w = shift;
        delete $self->streams->{$user}->{$w->id};
      }
    );

    $self->streams->{$user}->{$writer->id} = $writer;
    $self->push_fake_events($_->{id}, $writer) for @$conns;
  };
}


sub login {
  my $self = shift;
  open my $fh, '<', 'templates/login.html' or die $!;
  $self->html($fh);
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

  my $json = JSON::XS->new;
  my $data = [ map { $json->decode($_->[0]) } @$rows ];
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

sub user {
  my ($self, $req, $captures, $session) = @_;
  return $self->json($session);
}

1;
