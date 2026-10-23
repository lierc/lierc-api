# lierc-api in Go: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single Go binary, `lierc-api` with `api` and `stream` subcommands, that replaces `bin/api.psgi` and `bin/stream.psgi` with matching wire behavior and can run alongside the Perl servers.

**Architecture:** Shared `internal/` packages handle config, Perl-compatible sessions, passwords and ids, Postgres access and the liercd client. `internal/api` is a port of `API::Dispatch` and the controllers, using its own Router::Boom-style matcher. `internal/stream` is a port of `API::Stream`, with go-nsq on an ephemeral channel. Go tests run against embedded Postgres, an in-process nsqd and a fake liercd. A separate parity suite in CI diffs Go against the real Perl image.

**Tech Stack:** Go 1.27 (stdlib `net/http`), `github.com/jackc/pgx/v5`, `github.com/nsqio/go-nsq`, `github.com/nsqio/nsq/nsqd` (tests only), `github.com/fergusstrange/embedded-postgres` (tests only), `golang.org/x/crypto/blowfish`, a PKCS#7 signing library (`github.com/smallstep/pkcs7`), Docker Compose for the parity suite in GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-23-lierc-api-go-design.md`. The Perl source is the reference for every behavior; each task names the Perl file it ports.

## Global Constraints

- No schema changes. Use `lierc.sql` as is.
- JSON responses have `Content-Type: application/javascript;charset=utf-8`, and errors are `{"status":"error","error":<string|null>}`.
- Status codes, JSON shapes and value types, SSE framing and replay step order must match Perl. Error text must match only for the messages the Perl code writes itself (spec, "Error messages").
- Go's session cookie is `chats2`: value `<user>.<expiry unix>.<hex HMAC-SHA256("chats2|<user>|<expiry>", API_SECRET)>`, `Path=/`, `HttpOnly`, `Secure` iff `API_SECURE` is truthy, a 7-day sliding expiry re-issued on every logged-in response. Perl's `chats` cookie (`Plack::Middleware::Session::Cookie` 0.36) is read only, as a fallback when there's no valid `chats2`. It's never written, except as an expired cookie on logout.
- Go is served behind nginx at `/api/v2/` (the prefix is stripped), so handlers see the same paths as Perl.
- Passwords: bcrypt with cost 15, raw salt `sprintf("%16s", API_SECRET)`, returned as a lowercase hex digest.
- Env vars and defaults as in `lib/API/Config.pm`, plus `LISTEN` (`:5004` api / `:5003` stream) and `TRUSTED_PROXIES` (default `127.0.0.1`). The stream server always uses database `lierc`.
- liercd is at `http://$LIERCD_HOST:5005/`. The Imgur `Authorization` header is `Client-ID 033f98700d8577c`.
- The NSQ consumer subscribes to topic `logged` on its own channel, named `lierc-api-go-<random>#ephemeral`.
- Do not fix Perl quirks. If a quirk looks like a bug, keep it and leave a one-line comment naming the Perl source.

## Review Focus

1. **Tampered, expired, logged-out (`{}`) or garbage cookies** (`chats` or `chats2`) count as logged out (401). They never cause a 500 or a panic. A bad `chats2` next to a good `chats` falls back to `chats`. Tests: Task 2.
2. **Non-ASCII and percent-encoded channel or nick names** (`%23caf%C3%A9`, `#Foo`) are decoded, then lowercased with Unicode rules for channels, exactly as `lc decode utf8` does. Tests: Task 8.
3. **Concurrent writes to one SSE stream** (ping ticker, NSQ delivery and replay all at once) never interleave bytes within an event. Tests: Task 11, run under `-race`.
4. **A client that disconnects during replay, or a slow client**, gets cleaned up: it's deregistered, `save_channels` and `last_login` run once, and no goroutine leaks. Tests: Task 11.
5. **Logging out on Go while a valid `chats` cookie is still around**, followed by a request with no cookies, gives a 401. The logout response expires both cookies, so migration can't log the user back in. Tests: Tasks 2 and 6.

---

## File Structure

```
go.mod, go.sum
cmd/lierc-api/main.go             subcommand dispatch, wiring, graceful shutdown
internal/config/config.go         env → Config (+ SMTP_OPTS parsing)
internal/storable/storable.go     thaw (read-only) for {string: string} hashes only
internal/session/session.go       chats2 codec, chats (Perl) reader, middleware
internal/password/password.go     Digest::Bcrypt-compatible hash
internal/ids/ids.go               Data::UUID + Math::BaseConvert base-62 ids
internal/db/db.go                 pgx pool + the shared queries from API::DB
internal/liercd/liercd.go         liercd HTTP client + FindOrRecreate
internal/mail/mail.go             verify email via SMTP_OPTS
internal/proxy/proxy.go           trusted X-Forwarded-For → RemoteAddr, access log
internal/api/{app,router,respond}.go        pipeline (API::Dispatch, Routes, Responses)
internal/api/{auth,connection,private,channel,message,ignore,pref,webpush,image,apn}.go
internal/stream/{server,writer,replay,nsq,stats}.go
internal/testenv/{postgres,nsqd,fakeliercd,fakesmtp,fakeimgur}.go
testdata/fixtures/gen.pl          generates Perl golden fixtures
testdata/fixtures/*.json          committed fixture output
parity/                           compose file + parity test suite (build tag `parity`)
Dockerfile.go, .github/workflows/go.yml
```

---

### Task 1: Module, config, and Perl golden fixtures

**Files:**
- Create: `go.mod`, `internal/config/config.go`, `internal/config/config_test.go`, `testdata/fixtures/gen.pl`, `testdata/fixtures/README.md`, `testdata/fixtures/{cookies,passwords,ids}.json`, `.gitignore` entry `testdata/fixtures/local/`

**Interfaces:**
- Produces:
  - `config.Load(getenv func(string) string, mode string) (config.Config, error)` where `mode` is `"api"` or `"stream"`
  - `type Config struct { Base, LiercdHost, DBHost, DBUser, DBPass, DBName, Secret, Key string; Secure bool; NSQAddr string; SMTP SMTPOpts; FromAddress, VapidPublic string; NoSMTP bool; APN APNConfig; Listen string; TrustedProxies []string; ImgurBaseURL string }`
  - `type SMTPOpts struct { Host string; Port int; SSL bool; Hello, AuthUser, AuthPass string; Extra map[string]string }`
  - `type APNConfig struct { WebsiteName, WebsitePushID string; AllowedDomains []string; FormatString, ServiceURL, CertFile, KeyFile string }`
  - Fixture files (schemas below), read by Tasks 2–4.

- [ ] **Step 1: Write `config_test.go`**

  Tests: `TestDefaults` (empty env → `DBName "lierc"`, `Secret "changeme"`, `NSQAddr "127.0.0.1:4150"`, `Listen ":5004"` for api and `":5003"` for stream, `TrustedProxies []string{"127.0.0.1"}`, `SMTP.Host "localhost"`, `FromAddress "do-not-reply@relaychat.party"`, `ImgurBaseURL "https://api.imgur.com"`, APN defaults from `Config.pm`). `TestStreamIgnoresPostgresDB` (`POSTGRES_DB=other` → api `"other"`, stream `"lierc"`). `TestSMTPOpts`: `"Host,smtp.x,Port,2465,SSL,1,AuthUser,u,AuthPass,p,Hello,h"` parses fully; `"smtp,smtp.resend.com,SSL,1"` gives `Host ""`, `Extra{"smtp":"smtp.resend.com"}` (Perl ignores this key too); an odd count returns the error `SMTP_OPTS must be a comma separated list of key,value pairs`. `TestSecureTruthy`: `API_SECURE` values `""` and `"0"` → false (Perl truthiness), `"1"` → true. `TestAPNAllowedDomainsSplitOnSpace`.

- [ ] **Step 2: Run `go test ./internal/config/` and confirm it fails** because the package doesn't exist yet.

- [ ] **Step 3: Implement `config.Load`**

  `ImgurBaseURL` comes from `LIERC_IMGUR_URL`, a test-only override that isn't documented for production. `NoSMTP` is true when `LIERC_NO_SMTP` is non-empty.

- [ ] **Step 4: Run `go test ./internal/config/`.** Expected: PASS.

- [ ] **Step 5: Write `testdata/fixtures/gen.pl`**

  It installs nothing itself. The README documents the setup: `cpanm -L testdata/fixtures/local Plack Plack::Middleware::Session@0.36 Digest::Bcrypt Data::UUID Math::BaseConvert JSON::XS`, at the versions in `cpanfile.snapshot`. The script then:
  - **cookies.json:** runs the real `Plack::Middleware::Session::Cookie` (secret `"test-secret"`, the same options as `bin/api.psgi`) through `Plack::Test` with an app that sets `{user => $id}`, and another that deletes `user`. It records `[{session, set_cookie_header, cookie_value}]`, including a user id with non-ASCII characters and an empty session. `set_cookie_header` is used only to copy `chats`'s `Path`/`Domain` for the expiring cookie on logout.
  - **passwords.json:** `Util->hash_password` (load `lib/Util.pm`) for these secrets: `"changeme"`, `"exactly16chars!!"`, and `"a-secret-longer-than-sixteen"`. Each is combined with these passwords: `""`, `"hunter2"`, `"pässwörd"`, a 72-byte password and an 80-byte password.
  - **ids.json:** for 20 fixed hex strings (including all-zero, all-`f`, and leading zeros), `Math::BaseConvert::cnv($hex, 16, 62)`, plus 5 values from `Util->uuid`.

- [ ] **Step 6: Run `perl -Itestdata/fixtures/local/lib/perl5 -Ilib testdata/fixtures/gen.pl`** and check that the three JSON files are written and non-empty.

- [ ] **Step 7: Commit**

```bash
git add go.mod internal/config testdata/fixtures .gitignore
git commit -m "go: config loading and Perl golden fixtures"
```

---

### Task 2: Session cookies (`chats2`, plus reading Perl's `chats`)

**Files:**
- Create: `internal/storable/storable.go`, `internal/storable/storable_test.go`, `internal/session/session.go`, `internal/session/session_test.go`

**Interfaces:**
- Consumes: `testdata/fixtures/cookies.json`
- Produces:
  - `storable.DecodeHash(b []byte) (map[string]string, error)`: read-only. It errors on anything other than an `nfreeze` hash of string scalars.
  - `session.ReadPerl(cookieValue, secret string) (user string, ok bool)`: `ok` only when the HMAC is valid and the hash has a non-empty `user`.
  - `type session.Codec struct { Secret string; Secure bool; Now func() time.Time }`
  - `(c Codec) Encode(user string) string` (a `chats2` value expiring `Now()+7d`) and `(c Codec) Decode(value string) (user string, ok bool)`
  - `(c Codec) Middleware(next http.Handler) http.Handler`
  - `type session.Session struct{ ... }` with `User() string`, `SetUser(id string)`, `Clear()`, from `session.FromContext(ctx context.Context) *Session`

- [ ] **Step 1: Write the tests.**
  - `TestReadPerlCookies`: every fixture `cookie_value` with a user gives that user, and `{}` gives `ok == false`.
  - `TestReadPerlRejects`: a bad signature, truncated base64, a valid signature over non-hash Storable bytes, an empty string, and no separators all give `ok == false` without panicking.
  - `TestChats2RoundTrip`.
  - `TestChats2Rejects`: an expired value, a tampered user, a tampered expiry, a wrong secret, a user id containing `.`, and garbage are all rejected.
  - `TestMiddlewarePrefersChats2`: valid `chats2` for user A plus valid `chats` for user B gives A.
  - `TestMiddlewareMigrates`: only `chats` gives the user, and the response sets `chats2` (`Path=/`, `HttpOnly`, `Max-Age` 604800, `Secure` iff `Secure`) and doesn't set `chats`.
  - `TestMiddlewareBadChats2FallsBack` (Review Focus 1).
  - `TestMiddlewareSlidingExpiry`: every logged-in response re-issues `chats2` with a fresh expiry.
  - `TestMiddlewareClear` (Review Focus 5): after `Clear()`, the response sets both `chats2` and `chats` expired (`Max-Age=0`), and `chats` uses the `Path`/`Domain` from the fixture's `set_cookie_header`.
  - `TestMiddlewareNoSession`: no cookies are set.
  - Fuzz tests `FuzzDecodeHash` and `FuzzCodecDecode` must never panic.

- [ ] **Step 2: Run `go test ./internal/storable/ ./internal/session/`** and confirm it fails.

- [ ] **Step 3: Implement**

  For the `chats` layout, read `Plack/Middleware/Session/Cookie.pm` 0.36 in `testdata/fixtures/local`. It's expected to be `<time>:<base64>:<hmac_sha1_hex(base64, secret)>`, but the fixtures decide. For the thaw side of `Storable::nfreeze`, decode the magic header, then `SX_HASH` with `SX_SCALAR`/`SX_LSCALAR`/`SX_UTF8STR`/`SX_LUTF8STR` values; anything else is an error. Compare both HMACs in constant time. The middleware sets cookies just before the first byte of the response: wrap `http.ResponseWriter` so `WriteHeader` and `Write` add `Set-Cookie`, and handle `Flush` for SSE responses.

- [ ] **Step 4: Run `go test -race ./internal/storable/ ./internal/session/`.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: chats2 session cookie with Perl cookie migration`.

---

### Task 3: Passwords and ids

**Files:**
- Create: `internal/password/password.go`, `internal/password/password_test.go`, `internal/ids/ids.go`, `internal/ids/ids_test.go`

**Interfaces:**
- Consumes: `testdata/fixtures/passwords.json`, `testdata/fixtures/ids.json`
- Produces: `password.Hash(password, secret string) string`, `ids.FromHex(hex string) string`, `ids.New() string`

- [ ] **Step 1: Write the tests.** `TestHashMatchesPerl` covers every row in `passwords.json` and is skipped under `-short`, because cost 15 is slow. `TestFromHexMatchesPerl` covers every row in `ids.json`. `TestNewShape`: 1,000 values from `ids.New()` use only the fixture alphabet, fall within the fixture length range, and contain no duplicates.

- [ ] **Step 2: Run `go test ./internal/password/ ./internal/ids/`** and confirm it fails.

- [ ] **Step 3: Implement**

  `password.Hash` reproduces `Crypt::Eksblowfish::Bcrypt::bcrypt_hash({key_nul => 1, cost => 15, salt => <16 raw bytes>}, password)` and returns `hex` of the 23-byte result. Pad the salt as `fmt.Sprintf("%16s", secret)`, then copy how `Digest::Bcrypt` treats salts longer than 16 bytes (the fixtures settle this). Port the EksBlowfish setup and the 64× `"OrpheanBeholderScryDoubt"` encryption from `golang.org/x/crypto/bcrypt`, using `blowfish.NewSaltedCipher` and `blowfish.ExpandKey`. The key is the password with a NUL appended, truncated to 72 bytes. `ids.FromHex` uses `Math::BaseConvert`'s digit order for base 62. `ids.New` = `FromHex` of 16 random bytes in uppercase hex, the way `Data::UUID` `create_hex` produces it minus its `0x` prefix (check the prefix against the `Util->uuid` fixture samples).

- [ ] **Step 4: Run `go test ./internal/password/ ./internal/ids/`.** Expected: PASS. It's not short mode, so the cost-15 fixtures run.

- [ ] **Step 5: Commit** with the message `go: Perl-compatible password hashing and ids`.

---

### Task 4: Test environment, DB layer, liercd client

**Files:**
- Create: `internal/testenv/{postgres,nsqd,fakeliercd}.go`, `internal/db/db.go`, `internal/db/db_test.go`, `internal/liercd/liercd.go`, `internal/liercd/liercd_test.go`

**Interfaces:**
- Consumes: `config.Config`, `ids.New`
- Produces:
  - `testenv.Postgres(t testing.TB) *db.DB`: one embedded Postgres per test binary (`TestMain` helper `testenv.Main(m)`), a fresh database per test created from `lierc.sql`
  - `testenv.NSQD(t testing.TB) (tcpAddr string, publish func(topic string, body []byte))`: an in-process nsqd
  - `testenv.FakeLiercd(t testing.TB) *FakeLiercd` with `URL string`, `SetStatus(id string, statusJSON string)`, `Respond(method, path string, code int, body string)`, `Calls() []LiercdCall`, `type LiercdCall struct{ Method, Path, Body string }`. With no other setup, `/:id/status` returns 404 until `/:id/create` is called, after which it returns the status that was set.
  - `db.Open(ctx context.Context, cfg config.Config) (*db.DB, error)`, `type DB struct{ Pool *pgxpool.Pool }` and these methods (all `ctx` first), ported from `lib/API/DB.pm` and `lib/API/Stream/DB.pm`: `Ping`, `UserExists(id) (bool, error)`, `LookupUser(id) (*User, error)` with `type User struct{ ID, Username, Email string }`, `VerifyOwner(connID, user string) (bool, error)`, `Connections(user string) ([]Connection, error)` with `type Connection struct{ ID string; Config json.RawMessage }`, `LookupConfig(id) ([]byte, bool, error)`, `LookupOwner(id) (string, bool, error)`, `AddUser(username, email, hashed string) (id, token string, err error)`, `SaveConnection(id, user string, config []byte) error`, `DeleteConnection(id) error`, `GetToken(user) (string, error)`, `CheckToken(user, token string) (bool, error)`, `UpdateConfig(id string, config []byte) error`, `SaveLastLogin(user) error`
  - `liercd.New(host string) *Client`, `type Client struct{ BaseURL string; HTTP *http.Client }`, `(c *Client) Do(ctx, method, path string, body []byte) (*liercd.Response, error)`, `type Response struct{ Status int; Body []byte }`, `(c *Client) FindOrRecreate(ctx, d *db.DB, id string) ([]byte, error)`, which returns the status body

- [ ] **Step 1: Write the tests.** `db_test.go` covers each method against `testenv.Postgres`. It includes `TestCheckTokenSingleUse` (true, then false) and `TestVerifyOwnerOtherUser` (false). `liercd_test.go` checks that `FindOrRecreate` sends GET status (200 → returns it), GET 404 → POST `create` with the stored config as the body → GET status, and returns the errors `Connection does not exist` and `Unable to create new connection: <code> <reason>` on failures. The request `Content-Type` is `application/javascript`.

- [ ] **Step 2: Run `go test ./internal/db/ ./internal/liercd/`** and confirm it fails.

- [ ] **Step 3: Implement the testenv helpers, `db` and `liercd`.** The HTTP client timeout is 180 s.

- [ ] **Step 4: Run `go test -race ./internal/...`.** Expected: PASS. The first run downloads the embedded Postgres binaries.

- [ ] **Step 5: Commit** with the message `go: db layer, liercd client, test environment`.

---

### Task 5: API pipeline: router, auth gate, responses, proxy

**Files:**
- Create: `internal/api/app.go`, `internal/api/router.go`, `internal/api/respond.go`, `internal/api/app_test.go`, `internal/proxy/proxy.go`, `internal/proxy/proxy_test.go`

**Interfaces:**
- Consumes: `session.Codec`, `db.DB`, `liercd.Client`, `config.Config`
- Produces:
  - `api.New(cfg config.Config, d *db.DB, lc *liercd.Client, m mail.Mailer) http.Handler`. `mail.Mailer` is defined in Task 6; until then, accept `nil`.
  - `type App struct{ Cfg config.Config; DB *db.DB; Liercd *liercd.Client; Mail mail.Mailer; Imgur *http.Client }`
  - `type Request struct{ *http.Request; Params map[string]string; Session *session.Session; Body []byte }` with `Param(name string) (string, bool)` (query + urlencoded body, query first, as `Plack::Request->parameters` merges them)
  - `type Handler func(a *App, r *Request) (Response, error)`
  - `type Response interface{ Write(w http.ResponseWriter) }`, with constructors `JSON(v any)`, `JSONStatus(v any, code int)`, `OK(extra ...any)` (extra is key/value pairs merged into `{"status":"ok"}`), `NoContent()`, `NotFound()`, `Unauthorized(msg string)`, `Error(msg string)`, `Passthrough(status int, header http.Header, body []byte)`, `Stream(fn func(w io.Writer) bool)`
  - `type route struct{ Method, Pattern, Name string; Public bool; H Handler }`. The route table in `router.go` lists every route in `lib/API/Routes.pm`, in the same order, with `Public` set for `auth.login|register|logout` and `apn.package|log|register|unregister`. Later tasks fill in the `H` fields; until then they're `nil`.
  - `proxy.Wrap(trusted []string, next http.Handler) http.Handler`, which rewrites `RemoteAddr` from `X-Forwarded-For` (last hop) when the peer is trusted and writes an access log line.
  - `ErrorFromDB(err error) Response`: for a `*pgconn.PgError`, the error is `Detail` (JSON `null` when it's empty); otherwise the generic message.

- [ ] **Step 1: Write `app_test.go`** with a stub handler table:
  - `TestUnauthenticated`: no cookie → 401 `{"status":"error","error":"unauthorized"}` for `/auth`, `/health`, and an unknown path `/nope` (401, not 404).
  - `TestUnknownPathLoggedIn` → 404 `{"status":"error","error":"not found"}`.
  - `TestMethodMismatch`: `PATCH /auth` while logged in → 404.
  - `TestLogoutAnyMethod`: GET, POST and DELETE `/logout` all route.
  - `TestOwnerCheck`: `/connection/<other user's id>` → 401 whose error starts with `Invalid connection id '`.
  - `TestRestCaptures`: `/notification/web_push/https://x/y?z` captures `endpoint = "https://x/y"`, and `/image/` captures `url = ""`.
  - `TestHandlerError`: a handler error → 400 with that message.
  - `TestContentType`: `application/javascript;charset=utf-8`.
  - `TestNoContentBody`: 204 with an empty body.
  - `TestDeletedUserSession`: a cookie for a user id not in `"user"` → 401.

  `proxy_test.go`: a trusted peer has its XFF applied, and an untrusted peer has it ignored.

- [ ] **Step 2: Run `go test ./internal/api/ ./internal/proxy/`** and confirm it fails.

- [ ] **Step 3: Implement the pipeline from `lib/API/Dispatch.pm` and `lib/API/Responses.pm`**

  Match against the decoded `r.URL.Path`, the same as PSGI `PATH_INFO`. Router::Boom semantics: `:name` = `[^/]+`, `{name:.+}`/`{name:.*}` = a regex tail, method `undef` = any method, first match in table order. Read the body once into `Request.Body`. Recover from panics as 400, the same as Perl's `eval`.

- [ ] **Step 4: Run `go test -race ./internal/api/ ./internal/proxy/`.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: api request pipeline`.

---

### Task 6: Auth routes and mail

**Files:**
- Create: `internal/api/auth.go`, `internal/api/auth_test.go`, `internal/mail/mail.go`, `internal/mail/mail_test.go`, `internal/testenv/fakesmtp.go`

**Interfaces:**
- Consumes: Task 5's `Handler` and response constructors; `password.Hash`; `db` methods
- Produces: `type mail.Mailer interface{ Send(to, subject, body string) error }`, `mail.New(opts config.SMTPOpts, from string, disabled bool) Mailer`, `testenv.FakeSMTP(t) (opts config.SMTPOpts, messages func() []mail.Received)`; and handlers `auth.health|show|login|register|logout|token|verify`, ported from `lib/API/Controller/Auth.pm`

- [ ] **Step 1: Write the tests.**
  - `TestLoginByEmailAndUsername` and `TestLoginBadPassword` → 401 `Invalid email or password`.
  - `TestLoginSetsChats2`: the response sets a `chats2` that `Codec.Decode` accepts, and doesn't set `chats`.
  - `TestRegisterRequiredFields`: each missing or whitespace field → 400 `<field> is required`, checked in the order username, email, pass.
  - `TestRegisterBadEmail` → 400 `Invalid email address`.
  - `TestRegisterSendsMail`: FakeSMTP receives subject `Please verify your relaychat.party account`, From is `FromAddress`, quoted-printable, and the body exactly matches the `$auth_template` rendering with the name and the link `https://relaychat.party/api/verify?<token>`.
  - `TestRegisterNoSMTP`.
  - `TestRegisterDuplicateEmail` → 400.
  - `TestShow` gives `{"email","user","id"}`.
  - `TestLogoutClearsUser` and `TestLogoutWithoutSession` → `{"status":"ok"}`. `TestLogoutAfterMigration` (Review Focus 5): log in with a Perl `chats` fixture only, `/logout`, then replay the request with the cookie jar's *resulting* cookies, where both are gone → 401.
  - `TestTokenShape`: `token` plus 6 `extra`, all distinct, all valid for `CheckToken`.
  - `TestVerify` uses the raw query string as the token. For the unknown-token case, the expected result is whatever the parity suite records from Perl. Write the test with the value the Task 15 parity run shows; until then, use the code-reading result (an empty arrayref is truthy, so the response is ok and nothing is updated).
  - `TestHealth`.
  - `mail_test`: implicit TLS when `SSL`; AUTH when `AuthUser` is set; `Hello` is used in EHLO; when `Host` is empty, connect to `localhost` (Net::SMTP's fallback).

- [ ] **Step 2: Run `go test ./internal/api/ ./internal/mail/ -run 'Auth|Login|Register|Logout|Token|Verify|Health|Mail'`** and confirm it fails.

- [ ] **Step 3: Implement the handlers and `mail`.** Email validation should accept and reject the same addresses as `Data::Validate::Email::is_email`; table-test a dozen cases taken from that module's tests.

- [ ] **Step 4: Run the same command.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: auth routes and verification email`.

---

### Task 7: Connection and private-chat routes

**Files:**
- Create: `internal/api/connection.go`, `internal/api/private.go`, `internal/api/connection_test.go`

**Interfaces:**
- Consumes: Tasks 4–5
- Produces: handlers `connection.list|create|show|edit|delete|send` (port of `lib/API/Controller/Connection.pm`) and `private.list|create|delete` (port of `lib/API/Controller/Private.pm`)

- [ ] **Step 1: Write the tests** against FakeLiercd.
  - `TestCreate`: body passed to `POST <id>/create` byte for byte, row saved with the raw body, response `{"success":"ok","id":<base62>}`.
  - `TestCreateRequired`: `Host`, `Port`, `Nick` → `<f> is required`.
  - `TestCreateLiercdFails`: liercd 500 with body `boom` → 400 with error `boom`.
  - `TestCreateBadJSON` → 400 (any text).
  - `TestEditKeepsId`: PUT sends destroy then create with the same id and returns ok even when destroy fails.
  - `TestDelete`.
  - `TestSendToken`: no or used token → 400 `Invalid token`; a valid one → raw body to `<id>/raw` and `{"status":"ok","token":<new>}`.
  - `TestList` and `TestShow`, where `Config` is the stored JSON object.
  - `TestPrivates`: create → 204, a second create touches the time, list is oldest first with `nick`, `connection` and `time` (time format copied from the parity recording), delete → ok.
  - `TestCreateNotifies`: a `LISTEN highlights` connection receives a notification after create.

- [ ] **Step 2: Run `go test ./internal/api/ -run 'Create|Edit|Delete|Send|List|Show|Private'`** and confirm it fails.

- [ ] **Step 3: Implement.** `edit` calls the delete and create handlers with the same `Request` and ignores their results, as Perl does. Create uses the `id` capture when it's present.

- [ ] **Step 4: Run the same command.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: connection and private chat routes`.

---

### Task 8: Logs, history, read state, ignores

**Files:**
- Create: `internal/api/channel.go`, `internal/api/message.go`, `internal/api/ignore.go`, `internal/api/logs_test.go`

**Interfaces:**
- Consumes: Tasks 4–5
- Produces: handlers `channel.list|logs|logs_id|date|last|set_seen`, `message.log|missed|seen|highlight`, `ignore.list|create|delete`, ported from the matching `lib/API/Controller/*.pm` files, with SQL copied verbatim (converted to `$n` placeholders)

- [ ] **Step 1: Write the tests.** Seed the `log` rows with a helper in the test file.
  - `TestLogsLimitClamp`: default 50, `limit=500` → 150, `limit=0` → 50 (Perl `||`).
  - `TestLogsOrderAndShape`: newest first; `MessageId` is a JSON number; `Message` is a decoded object; `Highlight` is a JSON bool; no `Self`.
  - `TestLogsBefore` (`/events/:event`).
  - `TestChannelLowercaseUnicode`: `%23Caf%C3%89` queries `#café`.
  - `TestChannelList`: most recently active first.
  - `TestDateStream`: `text/event-stream;charset=utf-8`; one `event: log` per row, oldest first; the `:to` day is included; the stream closes when done. `text` and `sender` filters each work, alone and together.
  - `TestLast`: only PRIVMSGs from the past week, `\x02` around matches, limit default 5, max 20.
  - `TestSetSeen` and `TestSeen`.
  - `TestMissed`: counts `messages`/`events`/`highlights` as JSON numbers; an empty query → `{}`; another user's connection → 400 starting `Invalid connection (`.
  - `TestLog250` (data) and `TestLog251` (400 `Too many messages!`); `Self` is present; oldest first.
  - `TestHighlight`, with and without `:event`.
  - `TestIgnoreCreateListDelete`: delete uses the `from` *query param*, not the path segment. `TestIgnoreListColumns` returns every column of `ignore` (`SELECT i.*`).

- [ ] **Step 2: Run `go test ./internal/api/ -run 'Logs|Channel|Date|Last|Seen|Missed|Log2|Highlight|Ignore'`** and confirm it fails.

- [ ] **Step 3: Implement**

  `log.id` is `serial`, so it's emitted as a JSON number. Each value's JSON type must match the parity recording. Lowercasing uses `strings.ToLower` after decoding the path as UTF-8, and invalid UTF-8 is replaced the way `Encode::decode` does (U+FFFD). The date stream writes and flushes each event, and stops when a write fails.

- [ ] **Step 4: Run the same command.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: log, history, read-state and ignore routes`.

---

### Task 9: Preferences, web push, images

**Files:**
- Create: `internal/api/pref.go`, `internal/api/webpush.go`, `internal/api/image.go`, `internal/api/misc_test.go`, `internal/testenv/fakeimgur.go`

**Interfaces:**
- Consumes: Tasks 4–5, `Config.ImgurBaseURL`
- Produces: handlers `pref.list|show|upsert|unsubscribe`, `webpush.keys|list|upsert|delete`, `image.create|list|delete`; `testenv.FakeImgur(t) (baseURL string, uploads func() []ImgurUpload)`

- [ ] **Step 1: Write the tests.**
  - `TestPrefUpsertRawBody`: the body `"\"dark\""` is stored as-is and shown as `{"name":"theme","value":"\"dark\""}`.
  - `TestPrefShow404`.
  - `TestPrefList`.
  - `TestUnsubscribe`: sets `email` to `false` and returns 204.
  - `TestWebPushKeys` (`VAPID_PUBLIC`).
  - `TestWebPushUpsertByEndpoint`.
  - `TestWebPushDeleteRestOfPath`.
  - `TestImageUpload`: a multipart file under any field name is forwarded with the Imgur `Authorization` header; the row is saved from `data.link`/`data.deletehash`; Imgur's status, headers and body pass through.
  - `TestImageNoUpload` → 400 `No upload`.
  - `TestImageUploadImgurFails`: passed through, nothing saved.
  - `TestImageList`: limit default 15, max 50, `page` offset, newest first.
  - `TestImageDelete`: a URL that isn't yours → 404 `not found`; Imgur 200 → row deleted and passed through.

- [ ] **Step 2: Run `go test ./internal/api/ -run 'Pref|Unsub|WebPush|Image'`** and confirm it fails.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run the same command.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: preference, web push and image routes`.

---

### Task 10: Safari push (APN) routes

**Files:**
- Create: `internal/api/apn.go`, `internal/api/apn_test.go`, `internal/api/testdata/apn/` (a self-signed test cert, key and WWDR stand-in, generated by a `go:generate` script in the test file)

**Interfaces:**
- Consumes: Tasks 4–5, `Config.APN`
- Produces: handlers `apn.config|device|package|log|register|unregister` (port of `lib/API/Controller/APN.pm`). The WWDR intermediate path stays `.apn/AppleWWDRCA.pem`, relative to the working directory, and the icons come from `icon.iconset/`, also relative to the working directory.

- [ ] **Step 1: Write the tests.**
  - `TestAPNConfig`.
  - `TestDevice`: raw body token upserts.
  - `TestRegisterUnregister`: `Authorization: ApplePushNotifications <user>` with no session works, and a different scheme → 400 `Invalid authorization header`.
  - `TestLog` → 204.
  - `TestPackage`: the response is `application/zip`, and the zip contains `website.json` with the expected keys and `authenticationToken` = the body's `user`. It also contains the 6 `icon.iconset/icon_*.png`, and `manifest.json` mapping each path to its SHA-1 hex. `signature` is a DER PKCS#7 detached signature over `manifest.json` that verifies against the test cert and includes the intermediate cert.
  - `TestPackageBadJSON` → 400.

- [ ] **Step 2: Run `go test ./internal/api/ -run 'APN|Device|Register|Package'`** and confirm it fails.

- [ ] **Step 3: Implement** with `archive/zip` and `smallstep/pkcs7`, building everything in memory with no temp dir and no shell-outs. Keep the `Content-Lenght` header, with its comment.

- [ ] **Step 4: Run the same command.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: Safari push routes`.

---

### Task 11: Stream server

**Files:**
- Create: `internal/stream/server.go`, `writer.go`, `replay.go`, `nsq.go`, `stats.go`, `stream_test.go`

**Interfaces:**
- Consumes: `session.Codec`, `db.DB`, `liercd.Client`, `testenv.NSQD`, `testenv.FakeLiercd`
- Produces:
  - `stream.New(cfg config.Config, d *db.DB, lc *liercd.Client) *Server` (an `http.Handler`)
  - `(s *Server) Run(ctx context.Context)`: the 15 s ping loop
  - `(s *Server) Consume(ctx context.Context, nsqAddr string) error`: topic `logged`, an ephemeral channel per process
  - `(s *Server) Deliver(body []byte)`: delivers one NSQ message
  - Tunables as fields for tests: `PingInterval time.Duration` (15 s) and `WriteTimeout time.Duration` (3 min)

- [ ] **Step 1: Write the tests.**
  - `TestStatsKey`: `/stats` with the right `lierc-key` → per-user counts, or details with `?user=`; a wrong key → 400 `Invalid key`.
  - `TestUnauthorized` → 401.
  - `TestHandshake`: headers, then `:` + 2048 spaces + `\n`, then `event: ping\ndata: <float>\n\n`.
  - `TestReplayOrder`: two registered connections and one unregistered connection with channels, topics, 120 nicks and modes. Events arrive grouped by step: all CREATE/CONNECT first, then 001/005/CAP, JOIN (prefix = nick), 332+333, 353×3 (50, 50, 20) + 366, then 324, with no `id:` lines. The 333 params are copied as-is from the status JSON.
  - `TestReplayRecreates`: a connection liercd doesn't know about gets `create` with its stored config.
  - `TestReplayOneConnectionFails`: matches the behavior the parity suite records for Perl (code reading says no replay events are sent but the stream stays open). Pin whichever the parity suite shows.
  - `TestLiveDelivery`: publishing to the in-process nsqd delivers `event: irc\ndata: <exact bytes>\nid: <MessageId>\n\n` only to the owner's streams, to every one of them.
  - `TestTwoConsumersBothDeliver`: two `Server`s on the same nsqd both get every message (ephemeral channels).
  - `TestConcurrentWritesRace` (Review Focus 3): ping at 1 ms plus a flood of NSQ messages under `-race`, then parse the byte stream and check every event is well-formed.
  - `TestCloseSavesChannels` (Review Focus 4): disconnect mid-replay, then `Config.Channels` is updated from liercd status, `last_login` is set, the registry is empty, and `runtime.NumGoroutine` returns to the baseline.
  - `TestWriteTimeout`: a client that stops reading is dropped after `WriteTimeout`.

- [ ] **Step 2: Run `go test -race ./internal/stream/`** and confirm it fails.

- [ ] **Step 3: Implement**

  Port `lib/API/Stream/Events.pm`, `Writer.pm` and `Util::irc_event`. Each stream owns a single writer goroutine fed by a buffered channel, so writes are serialized. Use `http.ResponseController.SetWriteDeadline` for the timeout. Replay builds `Message.Time` as float seconds. `Deliver` decodes only `ConnectionId` and `MessageId`, and omits `id:` when `MessageId` is falsy (0 or missing), as Perl does.

- [ ] **Step 4: Run `go test -race ./internal/stream/`.** Expected: PASS.

- [ ] **Step 5: Commit** with the message `go: SSE stream server`.

---

### Task 12: Binary, Dockerfile, CI

**Files:**
- Create: `cmd/lierc-api/main.go`, `cmd/lierc-api/main_test.go`, `Dockerfile.go`, `.github/workflows/go.yml`

**Interfaces:**
- Consumes: everything above
- Produces: the `lierc-api api` and `lierc-api stream` commands, and the image `ghcr.io/lierc/lierc-api-go` (tags `latest` and the short sha)

- [ ] **Step 1: Write `main_test.go`.** `TestUnknownSubcommand` exits 2 with usage. `TestServesHealthUnauthorized` starts `api` on `127.0.0.1:0` against testenv and expects `GET /health` → 401.

- [ ] **Step 2: Run `go test ./cmd/...`** and confirm it fails.

- [ ] **Step 3: Implement `main.go`.** It loads config for the mode, opens the DB, and wraps the handler with `proxy.Wrap` and then the session middleware. In stream mode it also starts `Run` and `Consume`. SIGTERM triggers a graceful shutdown. Write `Dockerfile.go`: a multi-stage build that produces a static binary on `gcr.io/distroless/static`, copies `icon.iconset/`, uses `WORKDIR /opt/lierc-api`, sets `ENTRYPOINT ["/lierc-api"]` and `CMD ["api"]`, and exposes 5004 and 5003. Write `go.yml`: `go vet`, `go test -race ./...` (full, including cost-15 fixtures), and on master a push of `ghcr.io/lierc/lierc-api-go`.

- [ ] **Step 4: Run `go vet ./... && go test -race ./...`.** Expected: all pass.

- [ ] **Step 5: Commit** with the message `go: lierc-api binary, Dockerfile and CI`.

---

### Task 13: Parity suite against the Perl image

**Files:**
- Create: `parity/compose.yml`, `parity/parity_test.go` (build tag `parity`), `parity/scenarios.go`, `parity/compare.go`, `parity/README.md`; Modify: `.github/workflows/go.yml` (add a `parity` job)

**Interfaces:**
- Consumes: the Perl image `ghcr.io/lierc/lierc-api:latest`, built from this repo's `Dockerfile`; `Dockerfile.go`; `testenv` fakes, run as standalone binaries via `go run ./internal/testenv/cmd/fakes`
- Produces: `go test -tags parity ./parity/`, which passes when Go and Perl agree

- [ ] **Step 1: Write `compose.yml`.** It runs `postgres` (loaded with `lierc.sql`), `nsqd`, a `fakes` service (liercd on 5005, SMTP, Imgur), `perl-api` (Gazelle :5004), `perl-stream` (Twiggy :5003, the command from the infra unit), `go-api` (:5014) and `go-stream` (:5013). All share one env: the same `API_SECRET`, `LIERCD_HOST=fakes`, and `LIERC_IMGUR_URL=http://fakes:8081`. Perl ignores that variable, so Perl's image routes are compared only on the no-upload and not-yours paths.

- [ ] **Step 2: Write `scenarios.go`**

  It holds a list of `Scenario{Name string; Steps []Step}`. Each `Step` is a method, path, header, body and an optional `Capture`, which saves ids and tokens for later steps. Every route in `API::Routes` gets at least one success case and one error case, plus the Review Focus inputs. Each scenario runs twice from a fresh DB snapshot, once against Perl and once against Go. Each server keeps its own cookie jar. `compare.go` diffs the status code, `Content-Type`, and the JSON compared by meaning: key order ignored, value types strict. Cookies are not compared. Generated ids, tokens, timestamps and error text from library or crash paths are allowed to differ, and that allow-list is written next to each scenario.

- [ ] **Step 3: Write the cross-server tests.** `TestMigrationFromPerl`: register on Perl, call Go `/auth` with that jar, and expect the same user plus a `chats2` cookie; a second Go call with only `chats2` also works. `TestPerlLogoutCookie`: a `chats` for `{}` (after Perl `/logout`) → Go 401. `TestGoLogoutStaysLoggedOut`: migrate, log out on Go, and the next Go call with the jar → 401. `TestStreamParity`: connect one stream to each server as the same user, publish 20 messages to `logged`, and check both streams get all 20 in order. Replay comparison is per step, with order within a step ignored.

- [ ] **Step 4: Run `docker compose -f parity/compose.yml up -d --build && go test -tags parity ./parity/ -v`.** Expected: PASS. Every difference is either fixed in Go, or (only for library or crash error text) added to the allow-list with a comment. Go back and pin the Perl-recorded values from this run in Task 6's `TestVerify` and Task 11's `TestReplayOneConnectionFails`, and pin the JSON value types for timestamps in Tasks 7–9.

- [ ] **Step 5: Add the `parity` job to `go.yml`.** It runs the same commands on `ubuntu-latest`, which has Docker.

- [ ] **Step 6: Commit** with the message `go: Perl/Go parity suite`.
