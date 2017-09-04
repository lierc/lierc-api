package API::Config;

use JSON::XS;

our %DEFAULT = (
  base     => "",
  host     => "127.0.0.1",
  dbhost   => "127.0.0.1",
  dbuser   => undef,
  dbpass   => undef,
  dbname   => "lierc",
  secret   => "changeme",
  key      => "changeme",
  secure   => 0,
  nsqd     => "127.0.0.1",
  nsq_tail => "/usr/local/bin/nsq_tail",
  apn => {
    website_name => "Relaychat Party",
    website_pushid => "web.party.relaychat",
    allowed_domains => "https://relaychat.party",
    format_string => "https://relaychat.party/app/#!/%@/%@",
    push_url => "https://relaychat.party/api/notification/apn/push",
    cert_file => ".apn/apn.pem",
    key_file  => ".apn/apn.key",
  }
);

sub new {
  my $class = shift;
  bless {}, $class;
}

sub apn {
  my %def = %{ $DEFAULT{apn}};
  return +{
    website_name    => $ENV{APN_NAME} || $def{website_name},
    website_pushid  => $ENV{APN_PUSHID} || $def{website_pushid},
    allowed_domains => [split " ", ($ENV{APN_ALLOWED_DOMAINS} || $def{allowed_domains})],
    format_string   => $ENV{APN_FORMAT_STRING} || $def{format_string},
    push_url        => $ENV{APN_PUSH_URL} || $def{push_url},
    cert_file       => $ENV{APN_CERT_FILE} || $def{cert_file},
    key_file        => $ENV{APN_KEY_FILE} || $def{key_file},
  }
}

sub base     { $ENV{API_BASE}          || $DEFAULT{base}     }
sub host     { $ENV{LIERCD_HOST}       || $DEFAULT{host}     }
sub dbhost   { $ENV{POSTGRES_HOST}     || $DEFAULT{dbhost}   }
sub dbuser   { $ENV{POSTGRES_USER}     || $DEFAULT{dbuser}   }
sub dbpass   { $ENV{POSTGRES_PASSWORD} || $DEFAULT{dbpass}   }
sub dbname   { $ENV{POSTGRES_DB}       || $DEFAULT{dbname}   }
sub secret   { $ENV{API_SECRET}        || $DEFAULT{secret}   }
sub nsqhost  { $ENV{NSQD_HOST}         || $DEFAULT{nsqd}     }
sub nsq_tail { $ENV{NSQ_TAIL}          || $DEFAULT{nsq_tail} }
sub secure   { $ENV{API_SECURE}        || $DEFAULT{secure}   }
sub key      { $ENV{API_KEY}           || $DEFAULT{key}      }

sub dsn     {
  my $self = shift;
  sprintf "dbi:Pg:dbname=%s;host=%s", $self->dbname, $self->dbhost;
}

sub nsq_address {
  my $self = shift;
  sprintf "%s:4150", $self->nsqhost;
}

sub as_hash {
  my $self = shift;
  return map {
    $_ => $self->$_
  } qw(base host dsn dbuser dbpass dbhost secret secure apn);
}

1;
