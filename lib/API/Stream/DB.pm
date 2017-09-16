package API::Stream::DB;

use strict;
use warnings;

use AnyEvent;
use AnyEvent::Pg::Pool;
use JSON::XS;
use Role::Tiny;

sub pg {
  my $self = shift;
  $self->{dbh} ||= do {
    AnyEvent::Pg::Pool->new(
      {
        dbname => "lierc",
        $self->dbhost ? (host     => $self->dbhost) : (),
        $self->dbuser ? (user     => $self->dbuser) : (),
        $self->dbpass ? (password => $self->dbpass) : (),
      },
      on_transient_error => sub { warn "transient connect error",  },
      on_connect_error   => sub { warn $_[1]->dbc->errorMessage },
    );
  };
}

sub query {
  my ($self, $query, $bind, $cb) = @_;
  my $cv = AE::cv;
  $self->pg->push_query(
    query => [$query, @$bind],
    on_error => sub { $cv->croak },
    on_result => sub {
      my $value = eval { $cb->($_[2]) };
      if (my $err = $@) {
        $cv->croak($err);
      }
      else {
        $cv->send($value);
      }
    }
  );
  return $cv;
}

sub connections {
  my ($self, $user) = @_;

  return $self->query(
    q{SELECT id, config FROM connection WHERE "user"=$1},
    [$user],
    sub {
      [ map {
          { id => $_->[0], Config => decode_json($_->[1]) }
        } $_[0]->rows ]
    }
  );
}

sub logged_in {
  my ($self, $session) = @_;
  $self->lookup_user($session->{user});
}

sub lookup_user {
  my ($self, $id) = @_;

  return $self->query(
    q{SELECT * FROM "user" WHERE id=$1},
    [$id],
    sub { $_[0]->row(0) }
  );
}

sub lookup_owner {
  my ($self, $id) = @_;

  return $self->query(
    q{SELECT "user" FROM connection WHERE id=$1},
    [$id],
    sub {
      my $res = shift;
      $res->nRows > 0 ? ($res->row(0))[0] : ();
    }
  );
}

sub update_config {
  my ($self, $id, $config) = @_;

  return $self->query(
    q{UPDATE connection SET config=$1 WHERE id=$2},
    [$config, $id],
    sub {}
  );
}
sub save_last_login {
  my ($self, $user) = @_;

  return $self->query(
    q{UPDATE "user" SET last_login=NOW() WHERE id=$1},
    [$user],
    sub {}
  );
}

sub lookup_config {
  my ($self, $id) = @_;

  return $self->query(
    q{SELECT config FROM connection WHERE id=$1},
    [$id],
    sub { ($_[0]->row(0))[0] }
  );
}

1;
