package App;

use strict;
use warnings;

use Encode;
use LWP::UserAgent;
use Text::Xslate;
use Data::UUID;
use URL::Encode qw(url_decode);
use JSON::XS;
use List::Util qw(min);
use Math::BaseConvert;
use AnyEvent::Handle;
use DBIx::Connector;
use Digest;

use Class::Tiny qw(host dsn dbuser dbpass secret base), {
  streams => sub { {} },
};

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

sub ua {
  my $self = shift;
  $self->{ua} ||= LWP::UserAgent->new;
}

sub url {
  my ($self, $path) = @_;
  sprintf "http://%s/%s", $self->host, $path;
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
  $self->html("login", {self => $self});
}

sub events {
  my ($self, $req, $captures, $session) = @_;
  my $id = $captures->{id};
  my $nick = $captures->{nick};
  
  return sub {
    my $response = shift;
    my $writer = $response->([200, ["Content-Type", "text/event-stream"]]);
    $self->add_writer($id, $writer);
    $self->push_fake_events($id, $writer, $nick);
  };
}

sub uuid {
  return cnv(Data::UUID->new->create_hex, 16, 62);
}

sub add_writer {
  my ($self, $id, $writer) = @_;
  my $wid = $self->uuid;

  $self->streams->{$id} = {}
    unless defined $self->streams->{$id};

  $self->streams->{$id}->{$wid} = $writer;

  $writer->{handle}->on_error(sub {
    warn $_[2];
    delete $self->streams->{$id}->{$wid};
  });

  $writer->{handle}->on_eof(sub {
    warn "EOF";
    delete $self->streams->{$id}->{$wid};
  });

  return $wid;
}

sub push_fake_events {
  my ($self, $id, $writer, $nick) = @_;

  my $res = $self->find_or_recreate_connection($id);
  my $status = decode_json($res->content);

  if ($nick ne $status->{Nick}) {
    $writer->write(sprintf("data: %s\n\n", encode_json {
      Command => "NICK",
      Prefix  => [$nick, 'lies', 'lies'],
      Params  => [$status->{Nick}],
      Time    => time,
      Raw     => ":$nick!lies\@lies NICK $status->{Nick}",
    }));
  }

  for my $name (keys %{ $status->{Channels} }) {
    my $channel = $status->{Channels}{$name};
    $writer->write(sprintf("data: %s\n\n", encode_json {
      Command => "JOIN",
      Prefix  => [$status->{Nick}, 'lies', 'lies'],
      Params  => [$name],
      Time    => time,
      Raw     => ":$status->{Nick}!lies\@lies JOIN $name"
    }));

    if ($channel->{Topic}{Topic}) {
      $writer->write(sprintf("data: %s\n\n", encode_json {
        Command => "332",
        Prefix  => [undef, undef, "lies"],
        Params  => [$status->{Nick}, $channel->{Name}, $channel->{Topic}{Topic}],
        Time    => time,
        Raw     => ":lies 332 $channel->{Name} :$channel->{Topic}{Topic}"
      }));
    }

    if ($channel->{Nicks}) {
      my $nicks = join " ", keys %{ $channel->{Nicks} };
      $writer->write(sprintf("data: %s\n\n", encode_json {
        Command => "353",
        Prefix  => [undef, undef, "lies"],
        Params  => [$status->{Nick}, "=", $channel->{Name}, $nicks],
        Time    => time,
        Raw     => ":lies 353 $status->{Nick} = $channel->{Name} :$nicks"
      }));
      $writer->write(sprintf("data: %s\n\n", encode_json {
        Command => "366",
        Prefix  => [undef, undef, "lies"],
        Params  => [$status->{Nick}, $channel->{Name}, "End of /NAMES list."],
        Time    => time,
        Raw     => ":lies 366 $status->{Nick} $channel->{Name} :End of /NAMES list."
      }));
    }
  }
}

sub irc_event {
  my ($self, $msg) = @_;

  my $data = decode_json($msg);
  my $id = $data->{Id};

  if (my $streams = $self->streams->{$id}) {
    my $event = sprintf("data: %s\n\n", encode_json($data->{Message}));
    $_->write($event) for values %$streams;
  }
}

sub create {
  my ($self, $req, $captures, $session) = @_;

  my $id  = $self->uuid;
  my $res = $self->ua->request( HTTP::Request->new(
    POST => $self->url("$id/create"),
    ["Content-Type", "application/javascript"],
    $req->content
  ) );

  if ($res->code == 200) {
    my $user = $session->{user};
    $self->dbh->do(q{
      INSERT INTO connection
      (id, "user", config)
      VALUES(?,?,?)
      }, {}, $id, $user, $req->content
    );

    return $self->text($id);
  }

  return [
    $res->code,
    [$res->flatten],
    [$res->content]
  ];
}

sub destroy {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->ua->request( HTTP::Request->new(
    POST => $self->url("$id/destroy"),
  ) );

  if ($res->code == 200) {
    $self->dbh->do('DELETE FROM connection WHERE id=?', {}, $id);
    return $self->redirect("/");
  }

  return [
    $res->code,
    [$res->flatten],
    [$res->content]
  ];
}

sub raw {
  my ($self, $req, $captures) = @_;

  my $id  = $captures->{id};
  my $res = $self->ua->request( HTTP::Request->new(
    POST => $self->url("$id/raw"),
    ["Content-Type", "application/irc"],
    $req->content
  ) );

  return [
    $res->code,
    [$res->flatten],
    [$res->content]
  ];
}

sub list {
  my ($self, $req, $captures, $session) = @_;
  my $conns = $self->connections($session->{user});

  return $self->json($conns);
}

sub find_or_recreate_connection {
  my ($self, $id, $user) = @_;

  my ($row) = $self->dbh->selectall_array(
    "SELECT config FROM connection WHERE id=?",
    {}, $id
  );

  die "Connection does not exist"
    unless $row;

  my $res = $self->ua->get($self->url("$id/status"));
  return $res if $res->code == 200;

  $res = $self->ua->request( HTTP::Request->new(
    POST => $self->url("$id/create"),
    ["Content-Type", "application/javascript"],
    $row->[0]
  ) );

  die "Unable to create new connection: " . $res->status_line
    unless $res->code == 200;

  $res = $self->ua->get($self->url("$id/status"));
  return $res;
}

sub show {
  my ($self, $req, $captures, $session) = @_;

  my $id = $captures->{id};
  my $res = $self->ua->get($self->url("$id/status"));

  return [
    $res->code,
    [$res->flatten],
    [encode utf8 => $res->decoded_content]
  ];
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
    WHERE channel=?
    AND connection=?
    ORDER BY time DESC
    OFFSET ? LIMIT ?
    }, {}, url_decode($chan), $id, $start, $end - $start
  );

  return $self->not_found unless @$rows;

  my $data = [ map { $_->[0] } @$rows ];
  return $self->json($data);
}

sub logged_in {
  my ($self, $session) = @_;

  if (! defined $session) {
    warn "no session";
    return 0;
  }

  if (! defined $session->{user}) {
    warn "no user in session";
    return 0;
  }

  if (! $self->lookup_user($session->{user})) {
    warn "user not in data base: '$session->{user}'";
    return 0;
  }

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
  my $hashed = $self->hash_password($pass);

  my ($row) = $self->dbh->selectall_array(q{
    SELECT id FROM "user"
    WHERE email=? AND password=?},
    {}, $email, $hashed
  );

  if ($row) {
    $req->env->{'psgix.session'}->{user} = $row->[0];
    return $self->ok;
  }

  return $self->unauthorized;
}

sub add_user {
  my ($self, $email, $pass) = @_;
  my $hashed = $self->hash_password($pass);
  my $id = $self->uuid;

  $self->dbh->do(q{
    INSERT INTO "user"
    (id, email, password)
    VALUES(?,?,?)},
    {}, $id, $email, $hashed
  );

  return $id;
}

sub hash_password {
  my ($self, $password) = @_;

  my $secret = sprintf("%16s", $self->secret);
  my $bcrypt = Digest->new(
    'Bcrypt',
    cost => 15,
    salt => $secret
  );
  $bcrypt->add($password);
  $bcrypt->hexdigest;
}

sub lookup_user {
  my ($self, $id) = @_;
  return () unless defined $id;

  my ($user) = $self->dbh->selectall_array(q{
    SELECT * FROM "user" WHERE id=?},
    {}, $id
  );

  return $user;
}

sub register {
  my ($self, $req) = @_;

  my $email = $req->parameters->{email};
  my $pass = $req->parameters->{pass};

  if (defined $email and defined $pass) {
    my $id = $self->add_user($email, $pass);
    $req->env->{'psgix.session'}->{user} = $id;
    return $self->ok;
  }

  return $self->forbidden;
}

sub connections {
  my ($self, $user) = @_;
  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT id, config
    FROM connection
    WHERE "user"=?},
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

  my $rows = $self->dbh->selectall_arrayref(q{
    SELECT id
    FROM connection
    WHERE "user"=?
    AND id=?},
    {}, $user, $id
  );

  return @$rows > 0;
}

sub not_found {
  return [
    404,
    ["Content-Type", "application/javascript"],
    [encode_json {"status" => "not found"}]];
}

sub redirect {
  my ($self, $path) = @_;
  return [
    302,
    ["Location", $self->path($path)],
    ["go there"]];
}

sub unauthorized {
  return [
    401,
    ["Content-Type", "application/javascript"],
    [encode_json {"status" => "unauthorized"}]];
}

sub json {
  my ($self, $data) = @_;
  return [
    200,
    ["Content-Type", "application/javascript"],
    [encode_json $data],];
}

sub text {
  my ($self, $text) = @_;
  return [
    200,
    ["Content-Type", "text/plain"],
    [$text]];
}

sub nocontent {
  return [204, [], []];
}

sub ok {
  my $self = shift;
  return $self->json({status => "ok"});
}


sub html {
  my ($self, $template, $vars) = @_;
  my $html = $self->template->render("$template.html", $vars);
  return [
    200,
    ["Content-Type", "text/html;charset=utf-8"],
    [encode utf8 => $html]];
};

1;
