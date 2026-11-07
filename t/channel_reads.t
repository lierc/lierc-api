use strict;
use warnings;

use Test::More;
use API::Queries;
use API::Controller::Channel;

# The scrollback and search handlers run the shared SQL files, and a search
# spans at most 31 days.

{
  package FakeSth;
  sub new { bless { rows => $_[1] || [], app => $_[2] }, $_[0] }
  sub execute { my $s = shift; push @{ $s->{app}{executed} }, [@_]; 1 }
  sub fetchrow_arrayref { shift @{ $_[0]{rows} } }
  sub finish { 1 }
  package FakeDBH;
  sub new { bless { app => $_[1] }, $_[0] }
  sub prepare {
    my ($self, $sql, $attr) = @_;
    push @{ $self->{app}{prepared} }, [$sql, $attr];
    FakeSth->new([[7, '{"Command":"PRIVMSG"}', "c1", 1]], $self->{app});
  }
  package FakeWriter;
  sub new { bless { out => [], closed => 0 }, $_[0] }
  sub write { push @{ $_[0]{out} }, $_[1]; 1 }
  sub close { $_[0]{closed}++ }
  package FakeApp;
  sub new { my $app = bless { prepared => [], executed => [] }, $_[0]; $app }
  sub dbh { FakeDBH->new($_[0]) }
  sub json { $_[1] }
  sub event_stream { [200, []] }
  package FakeReq;
  sub new { my ($c, %p) = @_; bless { caps => $p{caps}, params => $p{params} || {} }, $c }
  sub captures { $_[0]{caps} }
  sub parameters { $_[0]{params} }
  sub session { { user => "u1" } }
}

sub run_date {
  my ($caps, $params) = @_;
  my $app = FakeApp->new;
  my $writer = FakeWriter->new;
  my $req = FakeReq->new(caps => { id => "c1", channel => "#Chan", %$caps }, params => $params);
  my $cb = API::Controller::Channel::date($app, $req);
  $cb->(sub { $writer });
  return ($app, $writer);
}

eval { API::Queries::sql("nope") };
like $@, qr/nope/, "an unknown query name dies";
like API::Queries::sql("channel_logs"), qr/SELECT/, "a known query is read";
unlike API::Queries::sql("channel_date"), qr/^--/m, "comment lines are stripped";

{
  my $app = FakeApp->new;
  my $res = API::Controller::Channel::logs($app, FakeReq->new(caps => { id => "c1", channel => "#Chan" }, params => { limit => 10 }));
  is $app->{prepared}[0][0], API::Queries::sql("channel_logs"), "logs prepares its file";
  ok $app->{prepared}[0][1]{pg_placeholder_dollaronly}, "with dollar-only placeholders";
  is_deeply $app->{executed}[0], ["#chan", "c1", 10], "logs binds channel, connection, limit";
  is $res->[0]{MessageId}, 7, "and returns the rows";
}

{
  my $app = FakeApp->new;
  API::Controller::Channel::logs_id($app, FakeReq->new(caps => { id => "c1", channel => "#Chan", event => 99 }));
  is $app->{prepared}[0][0], API::Queries::sql("channel_logs_before"), "logs_id prepares its file";
  ok $app->{prepared}[0][1]{pg_placeholder_dollaronly}, "with dollar-only placeholders";
  is_deeply $app->{executed}[0], ["#chan", "c1", 99, 50], "logs_id binds channel, connection, before, limit";
}

{
  my ($app, $w) = run_date({ from => "2026-01-01", to => "2026-01-31" }, {});
  is $app->{prepared}[0][0], API::Queries::sql("channel_date"), "date prepares its file";
  ok $app->{prepared}[0][1]{pg_placeholder_dollaronly}, "with dollar-only placeholders";
  is_deeply $app->{executed}[0], ["c1", "#chan", "2026-01-01", "2026-01-31", undef, undef],
    "31 days inclusive runs, with undef text and sender";
  is scalar @{ $w->{out} }, 1, "and streams a row";
  is $w->{closed}, 1, "then closes";
}

{
  my ($app) = run_date({ from => "2026-01-01", to => "2026-01-05" }, { text => "hi", sender => "bob" });
  is_deeply $app->{executed}[0], ["c1", "#chan", "2026-01-01", "2026-01-05", "hi", "bob"],
    "text and sender are bound";
}

{
  my ($app) = run_date({ from => "2026-01-01", to => "2026-01-05" }, { sender => "bob" });
  is_deeply $app->{executed}[0], ["c1", "#chan", "2026-01-01", "2026-01-05", undef, "bob"],
    "a missing text is undef";
}

{
  my ($app, $w) = run_date({ from => "2026-01-01", to => "2026-02-01" }, {});
  is scalar @{ $app->{prepared} }, 0, "32 days never prepares";
  is_deeply $w->{out}, [qq{event: search_error\ndata: {"error":"Search at most 31 days at a time"}\n\n}],
    "and writes the search_error event";
  is $w->{closed}, 1, "and closes";
}

{
  my ($app, $w) = run_date({ from => "2026-02-01", to => "2026-12-31" }, { text => "x" });
  is scalar @{ $app->{prepared} }, 0, "a long search with text is refused too";
}

for my $pair (["-infinity", "infinity"], ["20200101", "20200105"], ["2026-1-1", "2026-1-5"],
              ["2026-02-31", "2026-03-01"], ["2026-01-01", "infinity"], ["nonsense", "2026-01-01"], [undef, "2026-01-01"]) {
  my ($app, $w) = run_date({ from => $pair->[0], to => $pair->[1] }, { text => "foo" });
  my $label = join "..", map { defined $_ ? $_ : "undef" } @$pair;
  is scalar @{ $app->{prepared} }, 0, "$label never prepares";
  is_deeply $w->{out}, [qq{event: search_error\ndata: {"error":"Dates must be YYYY-MM-DD"}\n\n}],
    "$label writes the date error event";
  is $w->{closed}, 1, "$label closes";
}

done_testing;
