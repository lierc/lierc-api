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

The files: `cookies.json` and `ids.json` (below), and `auth.json` (signed
tokens, `is_email` and `irc_nick` answers).

## cookies.json

Array of `{name, session, set_cookie_header, cookie_value}`, produced by the real
`Plack::Middleware::Session::Cookie` with secret `"test-secret"`, `session_key`
`chats`, `expires` 7 days, `httponly` 1, `secure` off (the options the Perl API used, apart from `secure`).

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
