use strict;
use warnings;

use Test::More;
use Util;

is(Util->irc_nick("lee.a"), "lee_a", ". becomes _");
is(Util->irc_nick("2cool"), "_2cool", "a leading digit gets a _ prefix");
is(Util->irc_nick("2cool.x"), "_2cool_x", "both");
is(Util->irc_nick("."), "_", "only a .");
is(Util->irc_nick("Lee_A1"), "Lee_A1", "an already-safe name is unchanged");
is(Util->irc_nick("a" x 40), "a" x 32, "cut to 32");

my $digit32 = "1" . ("a" x 31);
my $nick = Util->irc_nick($digit32);
is($nick, "_1" . ("a" x 30), "32 characters starting with a digit stays within 32");
is(length $nick, 32, "length 32");

done_testing;
