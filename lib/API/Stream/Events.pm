package API::Stream::Events;

use strict;
use warnings;

use Util;
use Writer;
use JSON::XS;
use Role::Tiny;
use AnyEvent;

sub push_fake_events {
  my ($self, $writer, $conns) = @_;
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
    $self->push_modes   ($status{$_}, $writer) for @ids;
    undef $cv;
  });
}

sub push_connect {
  my ($self, $status, $writer) = @_;
  my $alias = $status->{Config}->{Alias} || $status->{Config}->{Host};
  $writer->irc_event($status->{Id}, liercd => "CREATE", $status->{Nick}, $alias);
  if ($status->{Status}->{Connected}) {
    $writer->irc_event($status->{Id}, liercd => "CONNECT");
  }
}

sub push_welcome {
  my ($self, $status, $writer) = @_;
  my $welcome = "Welcome to the Internet Relay Network $status->{Nick}";
  $writer->irc_event($status->{Id}, liercd => "001", $status->{Nick}, $welcome);

  if ( my @isupport = @{ $status->{Isupport} } ) {
    $writer->irc_event($status->{Id}, liercd => "005", $status->{Nick}, join " ", @isupport);
  }

  if ( my @caps = @{ $status->{CapsAcked} || [] } ) {
    $writer->irc_event($status->{Id}, liercd => "CAP", "*", "ACK", join " ", @caps);
  }
}

sub push_joins {
  my ($self, $status, $writer) = @_;
  my @channels = @{ $status->{Channels} };

  for my $channel (@channels) {
    $writer->irc_event($status->{Id}, $status->{Nick} => "JOIN", $channel->{Name});
  }
}

sub push_modes {
  my ($self, $status, $writer) = @_;
  my @channels = @{ $status->{Channels} };

  for my $channel (@channels) {
    if ($channel->{Mode}) {
      $writer->irc_event(
        $status->{Id}, liercd => "324",
        $channel->{Name}, $channel->{Mode}
      );
    }
  }
}

sub push_topics {
  my ($self, $status, $writer) = @_;
  my @channels = @{ $status->{Channels} };

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
  my @channels = @{ $status->{Channels} };
  for my $channel (@channels) {
    if ($channel->{Nicks}) {
      my @nicks = @{ $channel->{Nicks} };
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

sub irc_event {
  my ($self, $msg) = @_;

  my $data = decode_json($msg);
  my $cv = $self->lookup_owner($data->{ConnectionId});

  $cv->cb(sub {
    my $user = $_[0]->recv;
    return unless defined $user;

    if (my $streams = $self->streams->{$user}) {
      my $event = Util->event(irc => $msg, $data->{MessageId});
      for (values %$streams) {
        $_->write($event);
      }
    }
  });
}

sub start_pings {
  my $self = shift;

  $self->{ping} = AE::timer 0, 15, sub {
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
  my ($self, $session, $respond, $remote, $agent) = @_;

  my $user = $session->{user};
  my $cv = $self->connections($user);

  $cv->cb(sub {
    my $conns = $_[0]->recv;
    my $handle = $respond->($self->event_stream);

    my $writer = Writer->new(
      handle => $handle,
      remote => $remote,
      agent  => $agent,
      on_close => sub {
        my $w = shift;
        delete $self->streams->{$user}->{$w->id};
        $self->save_channels($user);
        $self->save_last_login($user);
      }
    );

    $writer->send_padding(2 << 10);
    $writer->ping;
    $self->streams->{$user}->{$writer->id} = $writer;
    $self->push_fake_events($writer, $conns);
    $self->save_last_login($user);
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
        if ( my @channels = @{ $status->{Channels} } ) {
          $conn->{Config}->{Channels} = [ map {$_->{Name}} @channels ];
          $self->update_config($status->{Id}, encode_json $conn->{Config});
        }
      });
    }
  });
}

sub stats {
  my ($self, $user) = @_;

  if ($user) {
    my @data;
    for my $writer ( values %{ $self->streams->{$user} } ) {
      push @data, {
        remote  => $writer->remote,
        agent   => $writer->agent,
        created => $writer->created,
      };
    }
    return \@data;
  }
  else {
    my %data;
    for my $user (keys %{ $self->streams }) {
      my $count = scalar keys %{ $self->streams->{$user} };
      $data{$user} = $count
      if $count > 0;
    }

    return \%data;
  }
}

around new => sub {
  my $orig = shift;
  my $self = $orig->(@_);
  $self->start_pings;
  return $self;
};

1;
