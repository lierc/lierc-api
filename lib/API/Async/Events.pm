package API::Async::Events;

use Util;
use Writer;
use JSON::XS;
use Role::Tiny;
use AnyEvent;

sub push_fake_events {
  my ($self, $writer, $conns, $options) = @_;
  my %status;

  my $cv = AE::cv;
  $cv->begin;

  # TODO: talk to sync API to initialize connections
  for my $conn (@$conns) {
    $cv->begin;
    my $id = $conn->{id};
    my $res = $self->find_or_recreate_connection($id);

    $res->cb(sub {
      $status{$id} = decode_json $_[0]->recv->content;
      $cv->end;
      undef $res;
    });
  }

  $cv->end;

  $cv->cb(sub {
    my @ids = grep { $status{$_}{Registered} } keys %status;
    $self->push_connect ($status{$_}, $writer) for keys %status;
    $self->push_welcome ($status{$_}, $writer) for @ids;
    $self->push_joins   ($status{$_}, $writer) for @ids;
    $self->push_topics  ($status{$_}, $writer) for @ids;
    $self->push_nicks   ($status{$_}, $writer) for @ids;
    undef $cv;
  });
}

sub push_connect {
  my ($self, $status, $writer) = @_;
  $writer->write( Util->event(
    connection => encode_json $status->{ConnectMessage}
  ) );
}

sub push_welcome {
  my ($self, $status, $writer) = @_;
  my $welcome = "Welcome to the Internet Relay Network $status->{Nick}";
  $writer->irc_event($status->{Id}, liercd => "001", $status->{Nick}, $welcome);
}

sub push_joins {
  my ($self, $status, $writer) = @_;
  my @channels = values %{ $status->{Channels} };

  for my $channel (@channels) {
    $writer->irc_event($status->{Id}, $status->{Nick} => "JOIN", $channel->{Name});
  }
}

sub push_topics {
  my ($self, $status, $writer) = @_;
  my @channels = values %{ $status->{Channels} };

  for my $channel (@channels) {
    if ($channel->{Topic}{Topic}) {
      $writer->irc_event(
        $status->{Id}, liercd => "332",
        $status->{Nick}, $channel->{Name}, $channel->{Topic}{Topic}
      );
      $writer->irc_event(
        $status->{Id}, liercd => "333",
        $status->{Nick}, $channel->{Name},
        $channel->{Topic}{User}, $channel->{Topic}{Time}
      );
    }
  }
}

sub push_nicks {
  my ($self, $status, $writer) = @_;
  my @channels = values %{ $status->{Channels} };
  for my $channel (@channels) {
    if ($channel->{Nicks}) {
      my @nicks = keys %{ $channel->{Nicks} };
      while (my @chunk = splice @nicks, 0, 50) {
        $writer->irc_event(
          $status->{Id}, liercd => "353",
          $status->{Nick}, "=", $channel->{Name}, join " ", @chunk
        );
      }
      $writer->irc_event(
        $status->{Id}, liercd => "366",
        $status->{Nick}, $channel->{Name}, "End of /NAMES list."
      );
    }
  }
}

sub connect_event {
  my ($self, $msg) =@_;

  my $data = decode_json($msg);
  my $cv = $self->lookup_owner($data->{Id});

  $cv->cb(sub {
    my $user = $_[0]->recv;
    if (my $streams = $self->streams->{$user}) {
      my $event = Util->event(connection => $msg);
      $_->write($event) for values %$streams;
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
      for (values %$streams) {
        $_->write($event);
        $_->last_id($msg_id) if $msg_id;
      }
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
  my ($self, $session, $respond, $options) = @_;

  my $user = $session->{user};
  my $cv = $self->connections($user);

  $cv->cb(sub {
    my $conns = $_[0]->recv;
    my $handle = $respond->($self->event_stream);

    my $writer = Writer->new(
      handle => $handle,
      on_close => sub {
        my $w = shift;
        delete $self->streams->{$user}->{$w->id};
        $self->save_channels($user);
        $self->save_last_id($user, $w->last_id);
      }
    );

    $self->streams->{$user}->{$writer->id} = $writer;
    $self->push_fake_events($writer, $conns, $options);
  });
}

sub save_channels {
  my ($self, $user) = @_;
  my $cv = $self->connections($user);
  $cv->cb(sub {
    my $conns = $_[0]->recv;

    for my $conn (@$conns) {
      my $cv = $self->request(GET => "$conn->{id}/status");
      $cv->cb(sub {
        my $status = decode_json $_[0]->recv->content;
        $status->{Config}->{Channels} = [ keys %{$status->{Channels}} ];
        $status->{Config}->{Nick} = $status->{Nick};
        $self->update_config($status->{Id}, encode_json $status->{Config});
      });
    }
  });
}

around new => sub {
  my $orig = shift;
  my $self = $orig->(@_);
  $self->start_pings;
  return $self;
};

1;
