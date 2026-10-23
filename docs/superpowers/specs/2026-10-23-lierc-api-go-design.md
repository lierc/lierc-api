# lierc-api in Go: design

Date: 2026-10-23
Status: draft for review

## Intent

Rewrite the two lierc-api servers, `bin/api.psgi` (JSON API) and `bin/stream.psgi` (SSE event stream), in Go. The cutover must be **invisible**: existing users keep their sessions and passwords, and the API is the same. The Go and Perl servers **run side by side** at first: Go is served under a new path, `/api/v2/`, and the old paths stay on Perl. A client moves by changing its API base path, and moves back the same way (user's decision).

`docs/lierc-api.md` is the API reference. Where it and the Perl source disagree, the Perl source wins.

## Goals

- One Go binary that replaces both Perl servers, behaving the same as they do on the wire.
- Go and Perl can serve the same users at the same time against the same Postgres, liercd and NSQ, with each client talking to one of them.
- A user who is logged in on Perl is still logged in the first time their client switches to `/api/v2/`.
- A parity harness that shows the two match before any client moves.

## Non-goals

- Porting `bin/archive`, `bin/cleanup`, `bin/lctl`, `bin/revive`. They stay in Perl.
- Fixing API quirks or bugs (see "Quirks to preserve"). Fixes come later, as separate changes, after Perl is retired.
- New features, new endpoints, or schema changes.

## Anti-goals

Even if everything else works, the rewrite fails if any of these happens:

- Any user is logged out, or has to reset a password, when their client switches to `/api/v2/`. (Switching back to v1 after the Perl cookie has expired, which happens after 7 days without a Perl request, does mean signing in again. That is accepted.)
- Logging out on Go leaves the user logged in on either server.
- Any client-visible difference: status code, JSON shape or value type, header that clients read, SSE framing or replay order. Error *text* is covered only partly; see "Error messages".
- Any schema change or new table. Perl must keep working against the same DB.
- Events dropped or split between servers on the stream side.

## Constraints

### Session cookies (user's decision: a separate Go cookie that reads the Perl one)

**Perl's cookie, `chats`:** `Plack::Middleware::Session::Cookie` 0.36 with `secret = API_SECRET`, 7-day expiry, HttpOnly, `Secure` when `API_SECURE` is set. The value is a timestamp, the base64 of `Storable::nfreeze(\%session)` and an HMAC-SHA1 hex signature; take the exact layout from the 0.36 source. The session hash only ever holds `{ user => "<id>" }`, or `{}` after logout.

**Go's cookie, `chats2`** (format is Claude's call): value = `<user id>.<expiry unix seconds>.<hex HMAC-SHA256 over "chats2|<user id>|<expiry>" keyed with API_SECRET>`. It has `Path=/`, HttpOnly, `Secure` when `API_SECURE` is set, and a 7-day expiry that is re-issued on every response from a logged-in session (sliding expiry, like Perl). Perl never reads it.

Go must:

- **Prefer `chats2`.** A valid, unexpired `chats2` sets the session user.
- **Migrate from `chats` once.** If there is no valid `chats2`, read `chats`: verify the HMAC, then decode the `nfreeze` image of a hash with zero or one string-valued key. If that yields a user, treat the user as logged in and issue `chats2`. Anything else counts as not logged in. Go only reads `chats`; it never writes or re-issues it, and it doesn't need a general Storable decoder.
- **Log out both.** `/logout` expires `chats2` and also sends an expired `chats` (`Path=/`, the same attributes as Perl's) so the user doesn't log straight back in through migration. An expired `Set-Cookie` needs no Storable encoding.
- **Login and register** issue `chats2` only.
- Pin the `chats` reading with golden tests built from cookies the real Perl middleware produced (including `{}` and a non-ASCII user id).

### Passwords

`Util::hash_password`: `Digest::Bcrypt` with cost 15, salt = `sprintf("%16s", API_SECRET)` (space-padded on the left to 16 bytes, used as the raw 16-byte salt), output = `hexdigest` of the raw bcrypt hash. Login compares that hex string to `"user".password`.

Go needs a bcrypt primitive that takes a raw salt and returns the raw digest. `golang.org/x/crypto/bcrypt` doesn't expose one, so vendor or adapt its core (or the `blowfish` package plus the EksBlowfish setup). Check how `Digest::Bcrypt` handles a secret longer than 16 characters and passwords over 72 bytes. Golden tests from real Perl output, including those edge cases.

### IDs

`Util::uuid` = `Data::UUID` hex converted to base 62 with `Math::BaseConvert`. It's used for user ids, verify tokens, write tokens, connection ids and stream ids. Go must produce ids with the same alphabet and length range: any random 128-bit value rendered through the same base-62 conversion. Note the digit order `Math::BaseConvert` uses.

### Running alongside Perl

- **NSQ:** `nsq_tail` runs with no `-channel`, so each stream process gets its own ephemeral channel and sees every message. Go must subscribe to topic `logged` on its own unique `#ephemeral` channel. A shared named channel would split events across processes.
- **All cross-request state is in Postgres** (users, tokens, connections, prefs, …). The only in-memory state is the stream registry and its ping timer, which are per-process in both implementations. A user with two clients, one on each server, sees the same data, and each of their streams gets every event.
- **liercd** is reached at `http://$LIERCD_HOST:5005/<id>/...`. Both servers may call `find_or_recreate_connection` for the same id at once. Perl already allows this race, and Go keeps the same behavior rather than adding locking.

### Configuration

Same env vars and defaults as `lib/API/Config.pm`: `API_BASE`, `LIERCD_HOST`, `POSTGRES_HOST/USER/PASSWORD/DB`, `API_SECRET`, `API_KEY`, `API_SECURE`, `NSQD_HOST` (port 4150), `IMGUR_KEY`, `SMTP_OPTS` (comma-separated key,value pairs passed to `Net::SMTP->new`: `Host`, `Port`, `SSL` for implicit TLS, `Hello`, plus `AuthUser`/`AuthPass`), `FROM_ADDRESS`, `VAPID_PUBLIC`, `LIERC_NO_SMTP`, `APN_*`. `NSQ_TAIL` becomes unused. Note that the stream app's Postgres pool hard-codes `dbname => "lierc"` and ignores `POSTGRES_DB`. Go copies this: the `stream` subcommand always uses database `lierc`, so the two stream servers can never read different databases (Claude's call).

New settings Perl doesn't need (Claude's call): `LISTEN` (default `:5004` for `api`, `:5003` for `stream`), because Perl sets its listen address on the plackup command line; and `TRUSTED_PROXIES` (default `127.0.0.1`), the addresses whose `X-Forwarded-*` headers are trusted.

### Reverse proxy

Perl trusts `X-Forwarded-*` only from `127.0.0.1` (for access logs and `/stats` remote addresses). Go does the same by default, and `TRUSTED_PROXIES` covers a Go container that the proxy doesn't reach over loopback.

While both run, `bin/lctl stats` (hard-coded to `127.0.0.1:5003`) sees only the streams on the server listening there. This is accepted for the side-by-side period (Claude's call).

## Approaches considered

1. **One Go binary with `api` and `stream` subcommands. (Chosen.)** Shared packages for session, DB, liercd and config. It deploys like today: two processes, on new ports. All routes are written first, then the parity harness gates moving clients over.

Traffic split: **a `/api/v2/` path per client (chosen)** over weighted per-request balancing. With per-request balancing, Go would have to write Perl's Storable cookies byte for byte. With a path per client, Go only reads them once, and keeps its own cookie.
2. **Route-by-route migration.** The proxy sends finished routes to Go one at a time. Real traffic reaches Go sooner, but the proxy config changes constantly and every request depends on both stacks for weeks. Not chosen.
3. **Two separate Go programs.** Mirrors the Perl layout, but duplicates the session, DB and config code that both apps share. Dropped.

## Decided design

### Layout and libraries (Claude's call)

- Go module at the repo root, with `cmd/lierc-api` (subcommands `api`, `stream`) and `internal/...` packages (`session`, `password`, `storable`, `db`, `liercd`, `api`, `stream`, `config`). Perl stays where it is until it's retired.
- `net/http` with Go 1.22+ method/pattern routing. Router::Boom semantics to match: `:param` matches one segment (`[^/]+`); `{endpoint:.+}` and `{url:.*}` take the rest of the path; `/logout` matches any method; unmatched paths give 404 `not found` *after* the auth check (so an unauthenticated unknown path is a 401, not a 404).
- `pgx/v5` (pool) for Postgres, `nsq-io/go-nsq` for NSQ, stdlib `net/smtp` + `mime/quotedprintable` for email, stdlib `archive/zip`, and a PKCS#7 detached-signature library (e.g. `go.mozilla.org/pkcs7` or a maintained fork) for the Safari push package, replacing the `openssl smime` and `zip` shell-outs.
- A new `Dockerfile` stage or file for the Go binary. The Perl image stays buildable.

### API server request pipeline (mirror `API::Dispatch`)

1. Load the session from `chats2`, falling back to `chats` (see "Session cookies").
2. Unless the route is `auth.login|register|logout` or `apn.package|log|register|unregister`, require a session `user` that exists in `"user"` (`SELECT * FROM "user" WHERE id=?`), else 401 `unauthorized`.
3. If the route has an `:id` capture, require that the connection belongs to the session user, else 401 `Invalid connection id '<id>'`. Check the exact string: the Perl interpolates `$req->captures->{id}` as a method call inside the string, so the real output may be `Invalid connection id 'API::Request=HASH(0x…)->captures->{id}'`. Whatever Perl emits is the spec. The parity harness should compare this message by pattern, since the address varies.
4. Run the handler. Any handler error becomes 400 `{"status":"error","error":…}` via the `API::Responses::error` rules: strip ` at <file> line N…`; for Postgres errors, use only the `DETAIL:` line (which yields JSON `null` when there is none); trim trailing newlines.
5. Issue `chats2` if there is a session user, or expire both cookies after logout.

Response helpers match `API::Responses` exactly, including `Content-Type: application/javascript;charset=utf-8` on JSON, the empty 204 body, and passthrough of upstream status/headers/body for Imgur.

### Stream server (mirror `API::Stream`)

- `GET /stats` with a `lierc-key` header equal to `API_KEY` returns the stats JSON. Otherwise 400 `Invalid key`. No session is needed.
- Every other path: 401 if not logged in. Otherwise, look up the user's connections, then respond `200 text/event-stream;charset=utf-8`, write `:` + 2048 spaces + `\n`, then a `ping`, register the stream, and start the replay.
- **Replay:** for each connection, `find_or_recreate_connection` concurrently. When *all* are done, emit in order: CREATE/CONNECT for every connection, then for registered ones the 001/005/CAP group, JOINs, 332/333, 353 chunks of 50 + 366, then 324. Match the Perl loop structure exactly: each step loops over all connections before the next step. Perl iterates `keys %status`, so connection order within a step is random. Any order is acceptable, but the step order is fixed. Replay events carry `Time` = current time with fractional seconds, and no `id:` line.
- **Live:** for each NSQ message, look up the owner of `ConnectionId` and write `event: irc\ndata: <raw message bytes>\nid: <MessageId>\n\n` to every stream of that user. The raw NSQ body is passed through untouched, not re-encoded.
- Ping every 15 s to all streams: `event: ping\ndata: <unix time with fraction>\n\n`.
- Close a stream on write error, client EOF, or a write blocked for 3 minutes. On close, deregister it, then `save_channels` (for each connection, GET status from liercd; if it has channels, set `Config.Channels` to their names and save) and update `last_login`.
- Update `last_login` on connect too.

### Error messages (user's decision)

Error text doesn't need to match exactly. What must match: the status code, the `{"status":"error","error":"<string>"}` shape, and the messages the Perl code writes on purpose (`Invalid token`, `Invalid email or password`, `<field> is required`, `Invalid email address`, `No upload`, `Too many messages!`, `Invalid key`, `Invalid authorization header`, `Invalid connection (…)`, `unauthorized`, `not found`, and so on). Text that comes from libraries or crashes can differ: JSON::XS parse errors, Perl runtime errors, Postgres `DETAIL` lines, and what Perl adds to liercd error bodies. Go should give a sensible message of its own, and should still pass liercd's response body through as the error when liercd fails. The harness checks the status code and shape for these, not the text.

### Quirks to preserve (incomplete; the harness finds the rest)

- `GET /health` needs a session.
- `DELETE /connection/:id/channel/:channel/ignore/:from` reads the `from` param, not the path segment.
- `PUT /connection/:id` ignores the results of its destroy and create steps.
- `/verify` treats the whole raw query string as the token, and returns a bare 401 `unauthorized` when the token is unknown. Check how Perl handles `selectcol_arrayref` returning an empty arrayref: it's truthy, so an unknown token may actually hit `UPDATE … WHERE id=NULL` and return ok. Match what Perl does.
- The `/v1/pushPackages` response has the misspelled header `Content-Lenght` alongside whatever Plack adds. The header names don't matter to clients; keep the zip body valid.
- The Imgur client id is hard-coded (`Client-ID 033f98700d8577c`), and `IMGUR_KEY` is unused. Keep both as they are.
- `GET /image` passes `page` straight into `OFFSET page*limit`.
- `Authorization: ApplePushNotifications <user>` on `/v1/devices/...` is trusted without verification.
- The registration email text, subject, from address, and verify link `https://relaychat.party/api/verify?<token>` are fixed.
- JSON value types: Perl/DBD::Pg may emit some numeric columns as JSON strings and some as numbers, and timestamps in Postgres text format. The Go output must match per field. Find these with the harness; don't assume.
- JSON key order differs between runs in Perl (hash order), so clients can't depend on it. The harness compares JSON by meaning, not byte for byte.

### Parity harness

- Docker Compose (or a Go test using testcontainers) that brings up Postgres with `lierc.sql`, a fake liercd (a small Go HTTP server with scripted `/status`, `/create`, `/destroy`, `/raw` responses and recorded calls), nsqd, a fake SMTP sink, and both servers. Imgur is faked by pointing the outbound base URL at a stub. This is a test-only override, off by default.
- A scripted scenario suite covers every route in `API::Routes`, plus error paths (missing params, wrong owner, bad token, Postgres constraint errors), run against Perl and Go from the same DB snapshot. It diffs the status, `Content-Type`, and the JSON compared by meaning, with listed exceptions for values that legitimately vary (generated ids, timestamps). Cookies are not compared, because the two servers use different ones by design.
- Migration tests: log in on Perl, then call Go with only the `chats` cookie and check that the user is logged in and `chats2` is issued. Log out on Go, then call Go again with the old `chats` value removed by the browser (simulate expiry) and check for a 401. A Perl cookie for `{}` gives a 401 on Go.
- Stream tests: connect to each server, check the padding, ping and replay sequence against the fake liercd, publish to NSQ, and check that both servers deliver the event to the same user, and that a user with two streams (one per server) gets it on both.
- It runs in CI, and the traffic shift waits until it's green.

### Rollout

**Production today** (from `aesthetes-chat/relaychat-infra`): everything runs on a single host. Both apps run as systemd units (`lierc-api.service`, `lierc-api-stream.service`) that start `ghcr.io/lierc/lierc-api:latest` with `--network host`. The api uses Gazelle on `:5004`, and the stream overrides the command to run Twiggy on `:5003`. Nginx on the same host proxies `/api/` to `localhost:5004` and `/api/events` to `localhost:5003`, so requests always come from 127.0.0.1, and the default `TRUSTED_PROXIES` is right.

**Side-by-side deployment (Claude's call):**
- A separate image, `ghcr.io/lierc/lierc-api-go`, built from a new Dockerfile (a static binary on a small base image). Two new units, `lierc-api-go.service` and `lierc-api-stream-go.service`, run it with `--network host` and `LISTEN=:5014` / `LISTEN=:5013`, using the same env file as Perl.
- Nginx gets `location /api/v2/events` → `localhost:5013/` (with the same SSE settings as `/api/events`) and `location /api/v2/` → `localhost:5014/`. Nginx strips the prefix, so Go serves the same paths as Perl. The existing `/api/` locations stay on Perl. These infra changes go in the infra repo, not this one.
- Some URLs are fixed outside any client and keep reaching Perl until Perl is retired: the verify link in the email (`/api/verify`) and the Safari push web service (`/v1/...` under `APN_SERVICE_URL`). Both use the shared DB, so this is harmless. At retirement, the old nginx locations point at Go.
- The `stream` command's container `CMD` is `lierc-api stream`, and the api's default is `lierc-api api`.

**Found in the infra config, to settle before rollout:**
- `lierc-api-stream.service` reads `--env-file /etc/lierc-api.env`, while the api and the only env template use `/etc/lierc.env`. Either the stream unit reads a hand-made file Ansible doesn't manage, or it runs without some settings. Check which on the host before the Go stream unit picks an env file.
- `SMTP_OPTS` in `lierc.env.j2` starts with `smtp,smtp.resend.com`, but `send_smtp` reads the host from the `Host` key. Perl ends up passing no host to `Net::SMTP` (which then falls back to its configured default), along with a stray `smtp` option. This may explain the recent email bug. Go parses `SMTP_OPTS` with the same keys as Perl (`Host`, `Port`, `SSL`, `Hello`, `AuthUser`, `AuthPass`). Fixing the env to `Host,smtp.resend.com` is an infra change that helps both servers.
- `POSTGRES_DB=lierc` in production, so the stream server's hard-coded `lierc` matches it.

1. Deploy Go next to Perl and add the `/api/v2/` locations. Point a test build of a client at `/api/v2/`.
2. Make each client's API base path configurable (the current UI and lierc-ngui; that work belongs to those repos), then switch clients over. Rollback means pointing a client back at `/api/`. Users whose Perl cookie expired in the meantime sign in again.
3. Retiring Perl is a separate decision: point the old nginx locations at Go then. The scripts stay in Perl either way.

## Left to the builder

- Exact package boundaries, error types, and logging format (access logs roughly equivalent to Plack's `AccessLog` are enough).
- Whether the harness is Compose plus a Go test runner or pure Go with testcontainers.
- How to generate the Perl golden fixtures (`chats` cookies, password hashes, ids), as long as they come from the real Perl code at the pinned module versions in `cpanfile.snapshot`.
- Connection pool sizes and HTTP timeouts. Perl uses LWP defaults (180 s) for liercd and Imgur; pick something no shorter.
