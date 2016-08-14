package API::Events;

use Util;
use JSON::XS;
use Role::Tiny;
use AnyEvent;

sub push_fake_events {
  my ($self, $id, $writer) = @_;

  my $res = $self->find_or_recreate_connection($id);
  my $status = decode_json($res->content);
  my $welcome = "Welcome to the Internet Relay Network $status->{Nick}";

  $writer->irc_event($id, liercd => "001", $status->{Nick}, $welcome);

  for my $name (keys %{ $status->{Channels} }) {
    my $channel = $status->{Channels}{$name};
    $writer->irc_event($id, $status->{Nick} => "JOIN", $name);

    if ($channel->{Topic}{Topic}) {
      $writer->irc_event(
        $id, liercd => 332,
        $status->{Nick}, $channel->{Name}, $channel->{Topic}{Topic}
      );
    }

    if ($channel->{Nicks}) {
      my $nicks = join " ", keys %{ $channel->{Nicks} };
      $writer->irc_event(
        $id, liercd => "353",
        $status->{Nick}, "=", $channel->{Name}, $nicks
      );
      $writer->irc_event(
        $id, liercd => "366",
        $status->{Nick}, $channel->{Name}, "End of /NAMES list."
      );
    }
  }
}

sub irc_event {
  my ($self, $msg) = @_;

  my $data = decode_json($msg);
  my $user = $self->lookup_owner($data->{Id});

  if (my $streams = $self->streams->{$user}) {
    my $msg_id = $data->{Message}->{Id};
    my $event = Util->event(irc => encode_json($data), $msg_id);
    $_->write($event) for values %$streams;
  }
}

sub start_pings {
  my $self = shift;

  $self->{ping} = AE::timer 0, 30, sub {
    for my $writers (values %{$self->streams}) {
      for my $writer (values %$writers) {
        $writer->ping;
      }
    }
  };
}

sub streams {
  my $self = shift;
  $self->{streams} ||= {};
}

after BUILD => sub {
  my $self = shift;
  $self->start_pings;
};

1;
