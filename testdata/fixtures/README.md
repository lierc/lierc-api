# Perl golden fixtures

A frozen record of the Perl API's behaviour, generated from the real Perl
code before it was deleted. The Go tests read these files to prove Go
answers as Perl did. They are no longer regenerated: the generator
(`gen.pl`) and the Perl code it ran are in git history.

They were made with Perl 5.42.0, Plack::Middleware::Session 0.36,
Digest::Bcrypt 1.212, JSON::XS 4.04, Data::UUID 1.227 and Math::BaseConvert
1.8, plus Data::Validate::Email, Data::Validate::Domain and Net::Domain::TLD
for `auth.json`. The cookie `expires`, the cookie id/time prefix and the uuid
samples were random per run.

The files: `ids.json` (below) and `auth.json` (signed tokens, `is_email`
and `irc_nick` answers). The Perl cookie fixtures went with the Perl
cookie code; both are in git history.

## ids.json

`{"cnv": [{hex, b62}], "uuid_samples": [...]}`.

- `cnv`: 20 entries; `b62` is `Math::BaseConvert::cnv($hex, 16, 62)` for a
  32-character lowercase hex string (includes all-zero, all-`f`, leading zeros).
- `uuid_samples`: 5 outputs of `Util->uuid` (random; only the shape is stable:
  base62 of a 128-bit value).
