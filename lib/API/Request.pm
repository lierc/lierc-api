package API::Request;

use parent 'Plack::Request';

sub new {
  my ( $class, $env, $captures, $session ) = @_;
  my $self = $class->SUPER::new($env);
  $self->{captures} = $captures;
  $self->{session}  = $session;
  return $self;
}

sub captures {
  my $self = shift;
  return $self->{captures} || {};
}

sub session {
  my $self = shift;
  return $self->{session} || {};
}

1;
