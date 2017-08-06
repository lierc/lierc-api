package API::Controller::WebPush;

use parent 'API::Controller';

API->register("webpush.upsert", __PACKAGE__);
API->register("webpush.list",   __PACKAGE__);

sub list {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $sth = $app->dbh->prepare_cached(q{
    SELECT key, auth, endpoint FROM web_push
    WHERE "user"=?
  });
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref({});
  $sth->finish;

  return $app->json($rows);
}

sub upsert {
  my ($app, $req) = @_;
  my $user = $req->session->{user};

  my $endpoint  = $req->parameters->{endpoint};
  my $auth      = $req->parameters->{auth};
  my $key       = $req->parameters->{key};

  my $sth = $app->dbh->prepare_cached(q{
    INSERT INTO web_push (endpoint, auth, key, "user")
      VALUES (?,?,?,?)
    ON CONFLICT ("user", endpoint)
      DO UPDATE SET auth=?, key=?
      WHERE web_push."user"=?
        AND web_push.endpoint=?
  });

  $sth->execute(
    $endpoint, $auth, $key, $user,
    $auth, $key, $user, $endpoint
  );

  $sth->finish;
  $app->nocontent;
}

1;
