package API::Async::Events;

use Util;
use Writer;
use JSON::XS;
use Role::Tiny;
use AnyEvent;

sub push_fake_events {
  my ($self, $id, $writer) = @_;

  my $cv = $self->find_or_recreate_connection($id);

  $cv->cb(sub {
    my $res = $_[0]->recv;

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
        my @nicks = keys %{ $channel->{Nicks} };
        while (my @chunk = splice @nicks, 0, 50) {
          $writer->irc_event(
            $id, liercd => "353",
            $status->{Nick}, "=", $channel->{Name}, @chunk
          );
        }
        $writer->irc_event(
          $id, liercd => "366",
          $status->{Nick}, $channel->{Name}, "End of /NAMES list."
        );
      }
    }
  });
}

sub irc_event {
  my ($self, $msg) = @_;

  my $data = decode_json($msg);
  my $cv = $self->lookup_owner($data->{ConnectionId});

  $cv->cb(sub {
    my $user = $_[0]->recv;
    if (my $streams = $self->streams->{$user}) {
      my $msg_id = $data->{MessageId};
      my $event = Util->event(irc => encode_json($data), $msg_id);
      $_->write($event) for values %$streams;
    }
  });
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

sub events {
  my ($self, $session, $respond) = @_;

  my $user = $session->{user};
  my $cv = $self->connections($user);

  $cv->cb(sub {
    my $conns = $cv->recv;
    my $handle = $respond->($self->event_stream);

    my $writer = Writer->new(
      handle => $handle,
      on_close => sub {
        my $w = shift;
        delete $self->streams->{$user}->{$w->id};
      }
    );

    $self->streams->{$user}->{$writer->id} = $writer;
    $self->push_fake_events($_->{id}, $writer) for @$conns;
  });
}

around new => sub {
  my $orig = shift;
  my $self = $orig->(@_);
  $self->start_pings;
  return $self;
};

1;
