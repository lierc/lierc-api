# Perl golden fixtures

JSON files generated from the real Perl code by `gen.pl`. Go tests read them to
prove compatibility with Perl. Regenerating is optional; the files are committed.

## Regenerating

From the repository root (needs Perl 5.42 with cpanm, no system libraries):

    cpanm -n -L testdata/fixtures/local Plack Plack::Middleware::Session@0.36 \
        Digest::Bcrypt Data::UUID Math::BaseConvert JSON::XS
    perl -Itestdata/fixtures/local/lib/perl5 -Ilib testdata/fixtures/gen.pl

`testdata/fixtures/local/` is git-ignored.

Versions used for the committed files: Plack::Middleware::Session 0.36
(exactly as in `cpanfile.snapshot`), Digest::Bcrypt 1.212, JSON::XS 4.04
(all as in `cpanfile.snapshot`), Data::UUID 1.227, Math::BaseConvert 1.8, Plack 1.0055 (snapshot has 1.0054;
not relevant to the output), Perl 5.42.0.

The cookie Set-Cookie `expires` and the cookie id/time prefix vary per run, and
the uuid samples are random, so regenerating changes those files.

## cookies.json

Array of `{name, session, set_cookie_header, cookie_value}`, produced by the real
`Plack::Middleware::Session::Cookie` with secret `"test-secret"`, `session_key`
`chats`, `expires` 7 days, `httponly` 1, `secure` off (same options as
`bin/api.psgi` apart from `secure`).

- `name`: case label.
- `session`: the session hash decoded from the cookie (`{"user": ...}`, or `{}`).
  Non-ASCII ids are Perl character strings (Storable UTF-8 flag set) and appear
  as JSON UTF-8 strings.
- `set_cookie_header`: the full `Set-Cookie` header value. It has `path=/` and
  no `Domain` attribute.
- `cookie_value`: the cookie value exactly as sent in the `Cookie` header (the
  part after `chats=`, still percent-escaped). Unescaped it is
  `<id>:<base64 Storable nfreeze>:<hmac_sha1_hex(base64, "test-secret")>`.

Cases: `login_ascii`, `login_numeric_id`, `login_non_ascii`, `login_email_like`,
`empty_session`, `logout_after_delete` (cookie after a logged-in session had
`user` deleted; session `{}`).

## ids.json

`{"cnv": [{hex, b62}], "uuid_samples": [...]}`.

- `cnv`: 20 entries; `b62` is `Math::BaseConvert::cnv($hex, 16, 62)` for a
  32-character lowercase hex string (includes all-zero, all-`f`, leading zeros).
- `uuid_samples`: 5 outputs of `Util->uuid` (random; only the shape is stable:
  base62 of a 128-bit value).
