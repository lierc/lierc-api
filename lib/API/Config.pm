package API::Config;

use JSON::XS;

our %DEFAULT = (
  base     => "",
  site_url => "https://relaychat.party",
  host     => "127.0.0.1",
  dbhost   => "127.0.0.1",
  dbuser   => undef,
  dbpass   => undef,
  dbname   => "lierc",
  secret   => "changeme",
  key      => "changeme",
  secure   => 0,
  smtp_opts => { Host => 'localhost' },
  from_address => 'do-not-reply@relaychat.party',
);

sub new {
  my $class = shift;
  bless {}, $class;
}

sub base     { $ENV{API_BASE}          || $DEFAULT{base}     }
sub site_url { $ENV{API_URL}           || $DEFAULT{site_url} }
sub host     { $ENV{LIERCD_HOST}       || $DEFAULT{host}     }
sub dbhost   { $ENV{POSTGRES_HOST}     || $DEFAULT{dbhost}   }
sub dbuser   { $ENV{POSTGRES_USER}     || $DEFAULT{dbuser}   }
sub dbpass   { $ENV{POSTGRES_PASSWORD} || $DEFAULT{dbpass}   }
sub dbname   { $ENV{POSTGRES_DB}       || $DEFAULT{dbname}   }
sub secret   { $ENV{API_SECRET}        || $DEFAULT{secret}   }
sub secure   { $ENV{API_SECURE}        || $DEFAULT{secure}   }
sub key      { $ENV{API_KEY}           || $DEFAULT{key}      }

# Key/value pairs for Net::SMTP->new, plus AuthUser/AuthPass for the optional
# AUTH exchange, eg
#   SMTP_OPTS=Host,smtp.example.com,Port,465,SSL,1,AuthUser,me,AuthPass,secret
sub smtp_opts {
  return $DEFAULT{smtp_opts} unless defined $ENV{SMTP_OPTS};

  my @opts = split /,/, $ENV{SMTP_OPTS};

  die "SMTP_OPTS must be a comma separated list of key,value pairs\n"
    if @opts % 2;

  return { @opts };
};

sub from_address { $ENV{FROM_ADDRESS} || $DEFAULT{from_address} }

# The origins the UI is served from, which may make state-changing
# requests (see API::SameOrigin), eg
#   API_ORIGINS=https://relaychat.party http://localhost:8080
# Defaults to API_URL alone.
sub origins {
  return [ split ' ', $ENV{API_ORIGINS} ] if defined $ENV{API_ORIGINS};
  return [ $ENV{API_URL} ] if defined $ENV{API_URL};
  return [];
}

sub dsn     {
  my $self = shift;
  sprintf "dbi:Pg:dbname=%s;host=%s", $self->dbname, $self->dbhost;
}

sub as_hash {
  my $self = shift;
  return map {
    $_ => $self->$_
  } qw(base site_url host dsn dbuser dbpass dbhost secret secure smtp_opts from_address);
}

1;
