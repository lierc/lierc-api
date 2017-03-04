package Writer;

use Util;
use Time::HiRes ();
use Class::Tiny qw(handle on_close last_id), {
  id      => sub { Util->uuid },
  created => sub { time },
};

sub BUILD {
  my $self = shift;

  $self->handle->{handle}->on_error(sub {
    $self->on_close->($self);
  });

  $self->handle->{handle}->on_eof(sub {
    $self->on_close->($self);
  });
}

sub irc_event {
  my $self = shift;
  $self->write( Util->irc_event( @_ ) );
}

sub write {
  my ($self, $line) = @_;
  $self->handle->write($line);
}

sub ping {
  my $self = shift;
  my $ping = Util->event(ping => Time::HiRes::time);
  $self->write($ping);
}

1;
