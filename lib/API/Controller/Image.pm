package API::Controller::Image;

use parent 'API::Controller';

use LWP::UserAgent;
use HTTP::Request::Common ();
use List::Util qw(min);
use JSON::XS;

API->register("image.create", __PACKAGE__);
API->register("image.list",   __PACKAGE__);
API->register("image.delete", __PACKAGE__);

sub delete {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $url  = $req->captures->{url};

  warn $url;

  my $sth = $app->dbh->prepare_cached(q!
    SELECT delete_hash FROM image
    WHERE "user"=? AND url=?
  !);

  $sth->execute($user, $url);
  my ($del) = $sth->fetchrow_array;

  if (!$del) {
    return $app->not_found;
  }

  my $ua = LWP::UserAgent->new;
  my $res = $ua->delete(
    "https://api.imgur.com/3/image/$del",
    Authorization  => "Client-ID 033f98700d8577c",
  );

  if ($res->code == 200) {
    $app->dbh->do(q!
      DELETE FROM image
      WHERE "user"=? AND url=?
    !, {}, $user, $url);
  }

  return [
    $res->code,
    [$res->flatten],
    [$res->content],
  ];
}

sub list {
  my ($app, $req) = @_;
  my $user  = $req->session->{user};
  my $limit = min( $req->parameters->{limit} || 15, 50);
  my $page  = $req->parameters->{page} || 0;

  my $sth = $app->dbh->prepare_cached(q!
    SELECT url, created
    FROM image
    WHERE "user"=?
    ORDER BY created DESC
    LIMIT ? OFFSET ?
  !);
  $sth->execute($user, $limit, $page * $limit);

  my $images = $sth->fetchall_arrayref({});
  $app->json($images);
}

sub create {
  my ($app, $req) = @_;
  my $user = $req->session->{user};
  my $key = $app->imgur_key;
  my $uploads = $req->uploads;

  if (!%$uploads) {
    return $app->error("No upload");
  }

  local $HTTP::Request::Common::DYNAMIC_FILE_UPLOAD = 1;

  my $ua = LWP::UserAgent->new;
  my $res = $ua->post(
    "https://api.imgur.com/3/upload",
    "Content-Type" => "form-data",
    Authorization  => "Client-ID 033f98700d8577c",
    Content => [
      map { $_ => [ $uploads->{$_}->path ] } keys %$uploads
    ]
  );

  if ($res->code == 200) {
    my $data = decode_json $res->decoded_content;
    my $sth = $app->dbh->prepare_cached(q!
      INSERT INTO image ("user", url, delete_hash)
      VALUES (?,?,?)
    !);
    $sth->execute(
      $user,
      $data->{data}->{link},
      $data->{data}->{deletehash}
    );
    $sth->finish;
  }

  return [
    $res->code,
    [$res->flatten],
    [$res->content],
  ];
}

1;
