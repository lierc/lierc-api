lierc-api
=========

relaychat's HTTP API, in Go: accounts and login, a user's IRC connection
settings (and driving `liercd` to run them), the event stream (server-sent
events) that delivers each user's IRC traffic to the browser, history and
search, preferences, web push and email. It replaced a Perl (Plack) API,
route for route; that code is in this repository's git history, and the
JSON fixtures in `testdata/fixtures` record its behaviour, which the Go tests
check against.

The same binary, `lierc-api`, also carries `lctl`, the admin command
(`lierc-api lctl help`).

Layout
------

- `cmd/lierc-api`: the server (`lierc-api stream`) and `lctl`.
- `internal/api`: the HTTP routes, behind a same-origin check.
- `internal/stream`: the event stream, NSQ consumers and the admin `/stats`.
- `internal/accounts`, `internal/passwords`, `internal/mail`: login,
  signup, email confirmation and password reset.
- `internal/connections`, `internal/connconfig`, `internal/liercd`:
  connection settings and liercd.
- `internal/db` (with `internal/db/sql`), `internal/archive`: Postgres.
- `internal/session`, `internal/storable`, `internal/auth`: the `chats`
  login cookie (still in the Perl API's format) and the login check.
- `lierc.sql`: the schema.

Running it
----------

The easiest way is the dev stack in `lierc-docker-compose`. To run it by
hand you need PostgreSQL with `lierc.sql` loaded, nsqd and liercd:

```
go build ./cmd/lierc-api
POSTGRES_HOST=127.0.0.1 LIERCD_HOST=127.0.0.1 NSQD_HOST=127.0.0.1 \
  LISTEN=127.0.0.1:5003 ADMIN_LISTEN=127.0.0.1:5009 ./lierc-api stream
```

`internal/config` lists every setting and its default.

Tests
-----

```
go test -p 1 ./...
```

The tests start their own embedded PostgreSQL and nsqd; `-p 1` keeps the
packages from starting several at once.
