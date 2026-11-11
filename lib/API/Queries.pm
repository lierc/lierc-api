package API::Queries;

use strict;
use warnings;
use File::Basename qw(dirname);
use File::Spec;

# The SQL of internal/db/sql/<name>.sql (shared with the Go server), read once. Whole-line "--" comments are
# dropped, so the placeholders they mention are never counted.

my $dir = File::Spec->catdir(dirname(__FILE__), "..", "..", "internal", "db", "sql");
my %cache;

sub sql {
  my ($name) = @_;
  die "Unknown query $name\n" unless defined $name && $name =~ /\A\w+\z/;
  return $cache{$name} //= do {
    my $file = File::Spec->catfile($dir, "$name.sql");
    open my $fh, "<:encoding(UTF-8)", $file or die "Unknown query $name: $!\n";
    my $text = do { local $/; <$fh> };
    $text =~ s/^[ \t]*--[^\n]*\n?//mg;
    $text;
  };
}

1;
