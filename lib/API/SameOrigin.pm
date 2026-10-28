package API::SameOrigin;

# Rejects requests that can change state (anything but GET, HEAD and
# OPTIONS) when the browser says they came from another site, so other
# sites' pages can't act with a user's session cookie.
#
# Browsers send Sec-Fetch-Site on every request; older ones only send
# Origin, which must then be one of `origins`. Requests with neither are
# not from a browser page (curl, scripts), so carry no cookie risk.

use strict;
use warnings;

use parent 'Plack::Middleware';
use Plack::Util::Accessor qw(origins);

my %SAFE_METHOD = map { $_ => 1 } qw(GET HEAD OPTIONS);
my %SAME_SITE   = map { $_ => 1 } qw(same-origin none);

sub call {
  my ($self, $env) = @_;

  return $self->app->($env)
    if $SAFE_METHOD{$env->{REQUEST_METHOD}} || $self->allowed($env);

  warn sprintf "refused cross-site %s %s (Sec-Fetch-Site: %s, Origin: %s)\n",
    $env->{REQUEST_METHOD}, $env->{PATH_INFO},
    map { $_ // "-" } @$env{qw(HTTP_SEC_FETCH_SITE HTTP_ORIGIN)};

  return [
    403,
    ["Content-Type", "application/javascript;charset=utf-8"],
    ['{"status":"error","error":"Cross-site request refused"}'],
  ];
}

sub allowed {
  my ($self, $env) = @_;

  my $site = $env->{HTTP_SEC_FETCH_SITE};
  return $SAME_SITE{$site} if defined $site;

  my $origin = $env->{HTTP_ORIGIN};
  return 1 unless defined $origin;

  return grep { $_ eq $origin } @{ $self->origins || [] };
}

1;
