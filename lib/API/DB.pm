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

  my $sth = $self->dbh->prepare(
    q{SELECT COUNT(id) FROM connection WHERE "user"=? AND id=?},
  );
  $sth->execute($user, $id);
  my ($count) = $sth->fetchrow_array;
  $sth->finish;

  return $count > 0;
}

sub connections {
  my ($self, $user) = @_;

  my $sth = $self->dbh->prepare(
    q{SELECT id, config FROM connection WHERE "user"=?},
  );
  $sth->execute($user);
  my $rows = $sth->fetchall_arrayref;
  $sth->finish;

  return [ map {
    { Id => $_->[0], Config => decode_json($_->[1]) }
  } @$rows ];
}

sub lookup_liercd {
  my ($self, $id) = @_;
  return undef unless defined $id;

  my $sth = $self->dbh->prepare(
    q{SELECT liercd FROM connection WHERE id=?}
  );
  $sth->execute($id);
  my ($liercd) = $sth->fetchrow_array;
  $sth->finish;

  return $liercd;
}

sub lookup_config {
  my ($self, $id) = @_;
  return () unless defined $id;

  my $sth = $self->dbh->prepare(
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

  my $sth = $self->dbh->prepare(
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

  # named columns: callers use only these, and SELECT * changes shape with
  # every schema change
  my $sth = $self->dbh->prepare(q{SELECT id, username, email FROM "user" WHERE id=?});
  $sth->execute($id);
  my $user = $sth->fetchrow_hashref;
  $sth->finish;

  return $user;
}

sub add_user {
  my ($self, $user, $email, $pass) = @_;
  my $hashed = Util->new_password_hash($pass);
  my $id = Util->uuid;

  my $sth = $self->dbh->prepare(
    q{INSERT INTO "user" (id, username, email, password_hash) VALUES(?,?,?,?)},
  );
  $sth->execute($id, $user, $email, $hashed);
  $sth->finish;

  return $id;
}

# Stores a new-style hash and drops the legacy one, so it can't be used again.
sub set_password_hash {
  my ($self, $id, $hash) = @_;
  $self->dbh->do(
    q{UPDATE "user" SET password_hash=?, password=NULL WHERE id=?},
    {}, $hash, $id
  );
}

# set_password_hash, but only if the stored hashes are still the given ones;
# returns how many rows changed (0 if someone else changed them first)
sub replace_password_hash {
  my ($self, $id, $hash, $old_hash, $old_password) = @_;
  return 0 + $self->dbh->do(
    q{UPDATE "user" SET password_hash=?, password=NULL
      WHERE id=? AND password_hash IS NOT DISTINCT FROM ? AND password IS NOT DISTINCT FROM ?},
    {}, $hash, $id, $old_hash, $old_password
  );
}

sub logged_in {
  my ($self, $session) = @_;

  return () unless defined $session && defined $session->{user};
  return () unless $self->lookup_user($session->{user});
  return 1;
}

sub save_connection {
  my ($self, $id, $user, $config) = @_;

  my $sth = $self->dbh->prepare(
    q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)},
  );
  $sth->execute($id, $user, $config);
  $sth->finish;
}

sub update_connection {
  my ($self, $id, $config) = @_;
  my $sth = $self->dbh->prepare(q{UPDATE connection SET config=? WHERE id=?});
  $sth->execute($config, $id);
  $sth->finish;
}

sub delete_connection {
  my ($self, $id) = @_;
  my $sth = $self->dbh->prepare(q{DELETE FROM connection WHERE id=?});
  $sth->execute($id);
  $sth->finish;
}

sub last_login {
  my ($self, $user) = @_;

  my $sth = $self->dbh->prepare(q{
    SELECT last_login FROM "user" WHERE id=?
  });
  $sth->execute($user);
  my ($last_login) = $sth->fetchrow_array;
  $sth->finish;

  return $last_login;
}

1;
