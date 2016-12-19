package API::Config;

use JSON::XS;

sub new {
  my $class = shift;
  bless { file => $config }, $class;
}

sub base    { $ENV{API_BASE}          || "" }
sub host    { $ENV{LIERCD_HOST}       || "127.0.0.1:5005" }
sub dbhost  { $ENV{POSTGRES_HOST}     || "127.0.0.1" }
sub dbuser  { $ENV{POSTGRES_USER}     || undef }
sub dbpass  { $ENV{POSTGRES_PASSWORD} || undef }
sub dbname  { $ENV{POSTGRES_DB}       || "lierc" }
sub secret  { $ENV{API_SECRET}        || "asdffdas" }
sub nsqhost { $ENV{NSQD_HOST}         || "" }

sub dsn     {
  my $self = shift;
  sprintf "dbi:Pg:dbname=%s;host=%s", $self->dbname, $self->dbhost;
}

sub nsq_address {
  my $self = shift;
  sprintf "%s:4150", $self->nsqhost;
}

sub nsq_path {
  my $self = shift;
  $ENV{NSQ_PATH} || "/usr/local/bin/nsq_tail";
}

sub as_hash {
  my $self = shift;
  return map {
    $_ => $self->$_
  } qw(base host dsn dbuser dbpass dbhost secret);
}

1;
