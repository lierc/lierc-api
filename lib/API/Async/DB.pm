package API::Async::DB;

use strict;
use warnings;

use AnyEvent;
use AnyEvent::DBI;
use JSON::XS;
use Role::Tiny;

sub dbh {
  my $self = shift;
  $self->{dbh} ||= AnyEvent::DBI->new(
    $self->dsn, $self->dbuser, $self->dbpass,
    RaiseError => 1,
    AutoCommit => 1,
  );
}

sub connections {
  my ($self, $user) = @_;
  my $cv = AE::cv;

  my $rows = $self->dbh->exec(
    q{SELECT id, config FROM connection WHERE "user"=?},
    $user, sub {
      my ($dbh, $rows, $rv) = @_;
      $cv->send([
        map {
          { id => $_->[0], Config => decode_json($_->[1]) }
        } @$rows
      ]);
    }
  );

  $cv;
}

sub logged_in {
  my ($self, $session) = @_;
  $self->lookup_user($session->{user});
}

sub lookup_user {
  my ($self, $id) = @_;
  my $cv = AE::cv;

  $self->dbh->exec(
    q{SELECT * FROM "user" WHERE id=?},
    $id, sub {
      my ($dbh, $rows, $rv) = @_;
      $cv->send($rows->[0]);
    }
  );

  $cv;
}

sub lookup_owner {
  my ($self, $id) = @_;

  my $cv = AE::cv;
  $cv->send() unless defined $id;

  $self->dbh->exec(
    q{SELECT "user" FROM connection WHERE id=?},
    $id, sub {
      my ($dbh, $row, $rv) = @_;
      $cv->send($row->[0][0]);
    }
  );

  $cv;
}

sub lookup_config {
  my ($self, $id) = @_;

  my $cv = AE::cv;
  $cv->send() unless $id;

  $self->dbh->exec(
    q{SELECT config FROM connection WHERE id=?},
    $id, sub {
      my ($dbh, $rows, $rv) = @_;

      $cv->send($rows->[0][0]);
    }
  );

  $cv;
}

1;
