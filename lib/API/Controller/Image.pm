package API::Controller::Image;

use parent 'API::Controller';

use LWP::UserAgent;
use HTTP::Request::Common ();
use JSON::XS;

API->register("image.create", __PACKAGE__);

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
    "https://api.imgur.com/3/image",
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
