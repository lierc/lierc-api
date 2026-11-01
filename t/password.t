use strict;
use warnings;

use Test::More;
use Util;

my $h = Util->new_password_hash("hunter22");
like($h, qr/^\$2b\$12\$[.\/A-Za-z0-9]{53}$/, "standard 2b cost-12 string");
isnt(Util->new_password_hash("hunter22"), $h, "fresh salt each time");
ok(Util->check_password_hash("hunter22", $h), "right password");
ok(!Util->check_password_hash("hunter23", $h), "wrong password");
ok(!Util->check_password_hash("hunter22", undef), "no hash");
ok(!Util->check_password_hash("hunter22", "deadbeef"), "legacy hex is not a bcrypt hash");

my $long = "x" x 100;
my $lh = Util->new_password_hash($long);
ok(Util->check_password_hash($long, $lh), "password over 72 bytes round-trips");

ok(Util->secure_eq("abc", "abc"), "equal strings");
ok(!Util->secure_eq("abc", "abd"), "different strings");
ok(!Util->secure_eq("abc", "abcd"), "different lengths");
ok(!Util->secure_eq(undef, "abc"), "undef");

done_testing;
