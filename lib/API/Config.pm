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
  nsqd     => "127.0.0.1",
  nsq_tail => "/usr/local/bin/nsq_tail"
);

sub new {
  my $class = shift;
  bless {}, $class;
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
  } qw(base host dsn dbuser dbpass dbhost secret);
}

1;
