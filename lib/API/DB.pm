package API::DB;

use Util;
use JSON::XS;
use DBIx::Connector;
use Encode;

use Role::Tiny;

sub dbh {
  my $self = shift;
  ($self->{connector} ||= DBIx::Connector->new(
    $self->dsn, $self->dbuser, $self->dbpass,
    {
      RaiseError => 1,
      AutoCommit => 1,
    }
  ))->dbh;
}

sub verify_owner {
  my ($self, $id, $user) = @_;

  my $rows = $self->dbh->selectall_arrayref(
    q{SELECT id FROM connection WHERE "user"=? AND id=?},
    {}, $user, $id
  );

  return @$rows > 0;
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

sub lookup_config {
  my ($self, $id) = @_;
  return () unless defined $id;

  my ($config) = $self->dbh->selectrow_array(
    q{SELECT config FROM connection WHERE id=?},
    {}, $id
  );

  return $config;
}

sub lookup_owner {
  my ($self, $id) = @_;
  return () unless defined $id;

  my ($user) = $self->dbh->selectrow_array(
    q{SELECT "user" FROM connection WHERE id=?},
    {}, $id
  );

  return $user;
}

sub lookup_user {
  my ($self, $id) = @_;
  return () unless defined $id;

  my $user = $self->dbh->selectrow_hashref(
    q{SELECT * FROM "user" WHERE id=?},
    {}, $id
  );

  return $user;
}

sub add_user {
  my ($self, $email, $pass) = @_;
  my $hashed = Util->hash_password($pass, $self->secret);
  my $id = Util->uuid;

  $self->dbh->do(
    q{INSERT INTO "user" (id, email, password) VALUES(?,?,?)},
    {}, $id, $email, $hashed
  );

  return $id;
}

sub logged_in {
  my ($self, $session) = @_;

  return () unless defined $session && defined $session->{user};
  return () unless $self->lookup_user($session->{user});
  return 1;
}

sub save_connection {
  my ($self, $id, $user, $config) = @_;

  $self->dbh->do(
    q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)},
    {}, $id, $user, $config
  );
}

sub delete_connection {
  my ($self, $id) = @_;
  $self->dbh->do(q{DELETE FROM connection WHERE id=?}, {}, $id);
}

sub find_logs {
  my ($self, $channel, $id, $limit) = @_;

  $self->dbh->selectall_arrayref(q{
    SELECT id, message, connection, self FROM log
      WHERE channel=? AND connection=?
      ORDER BY id DESC LIMIT ?
    }, {}, $channel, $id, $limit
  );
}

sub last_id {
  my ($self, $user) = @_;

  my ($last_id) = $self->dbh->selectrow_array(q{
    SELECT last_id FROM "user" WHERE id=?
  }, {}, $user);

  return $last_id;
}

1;
