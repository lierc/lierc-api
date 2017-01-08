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

  my $sth = $self->dbh->prepare_cached(
    q{SELECT COUNT(id) FROM connection WHERE "user"=? AND id=?},
  );
  $sth->execute($user, $id);
  my ($count) = $sth->fetchrow_array;
  $sth->finish;

  return $count > 0;
}

sub connections {
  my ($self, $user) = @_;

  my $sth = $self->dbh->prepare_cached(
    q{SELECT id, config FROM connection WHERE "user"=?},
  );
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref;
  $sth->finish;

  return [ map {
    { Id => $_->[0], Config => decode_json($_->[1]) }
  } @$rows ];
}

sub lookup_config {
  my ($self, $id) = @_;
  return () unless defined $id;

  my $sth = $self->dbh->prepare_cached(
    q{SELECT config FROM connection WHERE id=?}
  );
  $sth->execute($id);
  my ($config) = $sth->fetchrow_array;
  $sth->finish;

  return $config;
}

sub lookup_owner {
  my ($self, $id) = @_;
  return () unless defined $id;

  my $sth = $self->dbh->prepare_cached(
    q{SELECT "user" FROM connection WHERE id=?},
  );
  $sth->execute($id);
  my ($user) = $sth->fetchrow_array;
  $sth->finish;

  return $user;
}

sub lookup_user {
  my ($self, $id) = @_;
  return () unless defined $id;

  my $sth = $self->dbh->prepare_cached(q{SELECT * FROM "user" WHERE id=?});
  $sth->execute($id);
  my $user = $sth->fetchrow_hashref;
  $sth->finish;

  return $user;
}

sub add_user {
  my ($self, $user, $email, $pass) = @_;
  my $hashed = Util->hash_password($pass, $self->secret);
  my $id = Util->uuid;

  my $sth = $self->dbh->prepare_cached(
    q{INSERT INTO "user" (id, username, email, password) VALUES(?,?,?,?)},
  );
  $sth->execute($id, $user, $email, $hashed);
  $sth->finish;

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

  my $sth = $self->dbh->prepare_cached(
    q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)},
  );
  $sth->execute($id, $user, $config);
  $sth->finish;
}

sub delete_connection {
  my ($self, $id) = @_;
  my $sth = $self->dbh->prepare_cached(q{DELETE FROM connection WHERE id=?});
  $sth->execute($id);
  $sth->finish;
}

sub find_logs {
  my ($self, $channel, $id, $limit) = @_;

  my $sth = $self->dbh->prepare_cached(q{
    SELECT id, message, connection, self FROM log
      WHERE channel=? AND connection=?
      ORDER BY id DESC LIMIT ?
  });
  $sth->execute($channel, $id, $limit);
  my $logs = $sth->fetchall_arrayref;
  $sth->finish;

  return $logs;
}

sub last_id {
  my ($self, $user) = @_;

  my $sth = $self->dbh->prepare_cached(q{
    SELECT last_id FROM "user" WHERE id=?
  });
  $sth->execute($user);
  my ($last_id) = $sth->fetchrow_array;
  $sth->finish;

  return $last_id;
}

1;
