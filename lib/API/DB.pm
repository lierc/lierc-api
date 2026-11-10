package API::DB;

use Util;
use JSON::XS;
use API::StoredJSON;
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
    { Id => $_->[0], Config => API::StoredJSON::decode($_->[1]) }
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
  my $sth = $self->dbh->prepare(q{SELECT id, username, email, verified FROM "user" WHERE id=?});
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
    # verify_sent: signup sends the first verify email
    q{INSERT INTO "user" (id, username, email, password_hash, verify_sent, registered) VALUES(?,?,?,?,now(),now())},
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

# logged_in is the session's user row (id, username, email, verified), or
# nothing when logged out or the user is gone.
sub logged_in {
  my ($self, $session) = @_;

  return () unless defined $session && defined $session->{user};
  return $self->lookup_user($session->{user});
}

sub save_connection {
  my ($self, $id, $user, $config) = @_;

  my $sth = $self->dbh->prepare(
    q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)},
  );
  $sth->execute($id, $user, $config);
  $sth->finish;
}

# save_connection_limited saves the connection only if the user has fewer
# than $max, and says whether it did. The user's row is locked while it
# counts, so two creates at once are counted one after the other; a user
# that's gone saves nothing.
sub save_connection_limited {
  my ($self, $id, $user, $config, $max) = @_;

  my $dbh = $self->dbh;
  $dbh->begin_work;
  my $saved = eval {
    my $ok = 0;
    my ($exists) = $dbh->selectrow_array(q{SELECT 1 FROM "user" WHERE id=? FOR UPDATE}, undef, $user);
    if ($exists) {
      my ($count) = $dbh->selectrow_array(q{SELECT count(*) FROM connection WHERE "user"=?}, undef, $user);
      if ($count < $max) {
        $dbh->do(q{INSERT INTO connection (id, "user", config) VALUES(?,?,?)}, undef, $id, $user, $config);
        $ok = 1;
      }
    }
    $dbh->commit;
    $ok;
  };
  if (my $err = $@) {
    $dbh->rollback;
    die $err;
  }
  return $saved;
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

# Announces a change to a connection on the connections channel, which
# the stream server LISTENs on to tell the owner's open tabs
# (connection:create, :update, :delete). $action is create, update or
# delete.
sub notify_connection {
  my ($self, $user, $id, $action) = @_;
  my $sth = $self->dbh->prepare(q{SELECT pg_notify('connections', ?)});
  $sth->execute(JSON::XS::encode_json({ user => $user, connection => $id, action => $action }));
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
