package API::StoredJSON;

use strict;
use warnings;

use JSON::XS;

# JSON for Postgres columns (connection.config). DBD::Pg sends and returns
# text as Perl characters, so this is JSON as characters: encode_json's
# UTF-8 bytes would be encoded again on the way in, and every non-ASCII
# character stored twice-encoded (Go readers then see "ð°" for 🍰).
# HTTP bodies, to and from the UI and liercd, stay encode_json /
# decode_json bytes.

my $JSON = JSON::XS->new;

sub encode { $JSON->encode($_[0]) }
sub decode { $JSON->decode($_[0]) }

1;
