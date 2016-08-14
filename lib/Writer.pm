package Writer;

use Util;
use Class::Tiny qw(handle on_close), {
  id => sub { Util->uuid }
};

sub BUILD {
  my $self = shift;

  $self->handle->{handle}->on_error(sub {
    warn $_[2];
    $self->on_close->($self);
  });

  $self->handle->{handle}->on_eof(sub {
    warn "EOF";
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
  my $ping = Util->event(ping => time);
  $self->write($ping);
}

1;
