package Writer;

use Util;
use Time::HiRes ();
use Class::Tiny qw(handle on_close remote agent created), {
  id      => sub { Util->uuid },
};

sub BUILD {
  my $self = shift;

  $self->created(time);

  $self->handle->{handle}->wtimeout( 60 * 3 );
  $self->handle->{handle}->on_wtimeout(sub {
    warn "write timeout, closing";
    $_[0]->destroy;
    $self->on_close->($self);
  });

  $self->handle->{handle}->on_error(sub {
    $self->on_close->($self);
  });

  $self->handle->{handle}->on_eof(sub {
    $self->on_close->($self);
  });
}

sub close {
  my $self = shift;
  $self->handle->{handle}->destroy;
}

sub irc_event {
  my $self = shift;
  $self->write( Util->irc_event( @_ ) );
}

sub send_padding {
  my $self = shift;
  my $len  = shift;
  $self->write( ":" . (" " x $len) . "\n");
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
