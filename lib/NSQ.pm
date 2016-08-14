package NSQ;

use AnyEvent::Handle;
use Class::Tiny qw(on_error on_message path address topic);
use IPC::Open3;
use Symbol qw(gensym);

sub tail {
  my $class = shift;
  my $self = $class->new(@_);

  my ($w, $r, $err);
  $err = gensym;
  my @opts = ("-topic", $self->topic, "-nsqd-tcp-address", $self->address);
  open3($w, $r, $err, $self->path, @opts) or die $!;
  close($w);

  $self->{handle} = AnyEvent::Handle->new(
    fh => $r,
    on_error => sub {
      my ($hdl, $fatal, $msg) = @_;
      $hdl->destroy;
      undef $err;
      $self->on_error->($msg) if $self->on_error;
    },
    on_eof => sub {
      my ($hdl, $fatal, $msg) = @_;
      $hdl->destroy;
      undef $err;
      $self->on_error->("EOF") if $self->on_error;
    }
  );

  $self->read_line();
  return $self;
}

sub DESTROY {
  my $self = shift;
  warn "Closing NSQ";
  $self->{handle}->destroy if $self->{handle};
}

sub read_line {
  my $self = shift;

  $self->{handle}->push_read(line => sub {
    $self->on_message->($_[1]) if $self->on_message;
    $self->read_line;
  });
}

1;
