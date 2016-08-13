package NSQ;

use AnyEvent;
use Class::Tiny qw(fh on_error on_message);

sub tail {
  my $class = shift;
  my $self = $class->new(@_);

  $self->{handle} = AnyEvent::Handle->new(
    fh => $self->fh,
    on_error => sub {
      my ($hdl, $fatal, $msg) = @_;
      $hdl->destroy;
      warn $msg;
      $self->{on_error}->($msg) if $self->{on_error};
    },
    on_eof => sub {
      my ($hdl, $fatal, $msg) = @_;
      $hdl->destroy;
      warn "EOF";
    }
  );

  $self->read_line();
  return $self;
}

sub read_line {
  my $self = shift;

  $self->{handle}->push_read(line => sub {
    $self->{on_message}->($_[1]) if $self->{on_message};
    $self->read_line;
  });
}

1;
