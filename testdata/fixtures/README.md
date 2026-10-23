# Perl golden fixtures

JSON files generated from the real Perl code by `gen.pl`. Go tests read them to
prove compatibility with Perl. Regenerating is optional; the files are committed.

## Regenerating

From the repository root (needs Perl 5.42 with cpanm, no system libraries):

    cpanm -n -L testdata/fixtures/local Plack Plack::Middleware::Session@0.36 \
        Digest::Bcrypt Data::UUID Math::BaseConvert JSON::XS \
        Data::Validate::Email@0.06 Data::Validate::Domain@0.15 Net::Domain::TLD@1.75
    perl -Itestdata/fixtures/local/lib/perl5 -Ilib testdata/fixtures/gen.pl

`testdata/fixtures/local/` is git-ignored.

`gen.pl` also writes `internal/api/tlds.go` (the TLD set of Net::Domain::TLD, used by
the Go email validator). After regenerating run `gofmt -w internal/api/tlds.go`.

Versions used for the committed files: Plack::Middleware::Session 0.36
(exactly as in `cpanfile.snapshot`), Digest::Bcrypt 1.212, JSON::XS 4.04,
Data::Validate::Email 0.06, Data::Validate::Domain 0.15, Net::Domain::TLD 1.75
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

## passwords.json

Array of `{secret, password, hash}` from `Util->hash_password($password, $secret)`
(bcrypt cost 15, salt `sprintf("%16s", $secret)`, lowercase hex digest).

- 3 secrets (`changeme`, `exactly16chars!!`, `fifteen_chars!!`) x 5 passwords
  (`""`, `hunter2`, `pässwörd`, 72 x `a`, an 80-byte `a..z` cycle) = 15 entries.
- Non-ASCII values are stored as UTF-8 JSON strings. Perl hashed the
  **UTF-8 encoded bytes** (`Encode::encode("UTF-8", ...)`), which is what the
  web form delivers: undecoded bytes. Go must hash the UTF-8 bytes of the string.
- Secrets longer than 16 bytes are deliberately absent: `Digest::Bcrypt` dies with
  `Salt must be exactly 16 octets long`, so Perl cannot hash with them. `gen.pl`
  asserts this for `a-secret-longer-than-sixteen`.
- bcrypt uses only the first 72 bytes of the password, so the 72- and 80-byte
  `a`-based passwords are the boundary check.

## ids.json

`{"cnv": [{hex, b62}], "uuid_samples": [...]}`.

- `cnv`: 20 entries; `b62` is `Math::BaseConvert::cnv($hex, 16, 62)` for a
  32-character lowercase hex string (includes all-zero, all-`f`, leading zeros).
- `uuid_samples`: 5 outputs of `Util->uuid` (random; only the shape is stable:
  base62 of a 128-bit value).

## emails.json

Array of `{email, valid}` where `valid` is whether
`Data::Validate::Email::is_email($email)` returns a defined value (the check in
`Auth.pm` register). 71 cases: plain and malformed addresses, TLDs (gTLD, ccTLD,
`.party`, bogus `.zzzz`), case, subdomains, trailing dot, IP literals, quoted
local parts, `+` tags, whitespace, extra `@` (Perl's `split` drops trailing empty
fields, so `a@example.com@` is valid), and label/length limits.
