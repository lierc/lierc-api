lierc-api
=========

relaychat's HTTP API, in Go: accounts and login, a user's IRC connection
settings (and driving `liercd` to run them), the event stream (server-sent
events) that delivers each user's IRC traffic to the browser, history and
search, preferences, web push and email. It replaced a Perl (Plack) API,
route for route; that code is in this repository's git history, and the
JSON fixtures in `testdata/fixtures` record its behaviour, which the Go tests
check against.

The image also carries `lctl`, the admin command (`lctl help`), a binary of
its own.

Layout
------

- `cmd/lierc-api`: the server (`lierc-api`, no arguments).
- `cmd/lctl`, `internal/lctl`: the admin command; `internal/admin`: the
  admin JSON API it talks to.
- `internal/api`: the HTTP routes, behind a same-origin check.
- `internal/stream`: the event stream, NSQ consumers and the admin `/stats`.
- `internal/accounts`, `internal/passwords`, `internal/mail`: login,
  signup, email confirmation and password reset.
- `internal/connections`, `internal/connconfig`, `internal/liercd`:
  connection settings and liercd.
- `internal/db` (with `internal/db/sql`), `internal/archive`: Postgres.
- `internal/login`, `internal/auth`: login records (one row per browser)
  and the `chats` cookie, and the login check.
- `internal/db/migrations`: the schema, as numbered migrations lierc-api
  applies at startup (see Schema changes).

Running it
----------

The easiest way is the dev stack in `lierc-docker-compose`. To run it by
hand you need an empty PostgreSQL database (lierc-api creates the
schema), nsqd and liercd:

```
go build ./cmd/lierc-api ./cmd/lctl
POSTGRES_HOST=127.0.0.1 LIERCD_HOST=127.0.0.1 NSQD_HOST=127.0.0.1 \
  LISTEN=127.0.0.1:5003 ADMIN_LISTEN=127.0.0.1:5009 ./lierc-api
```

`internal/config` lists every setting and its default.

Schema changes
--------------

The schema is the files in `internal/db/migrations`, `0001_initial.sql`
onwards. At startup, before it listens, lierc-api applies the ones the
database hasn't had, each in its own transaction, and records them in
`schema_migrations`; if one fails it exits. A second lierc-api starting at
the same time waits for the first.

To change the schema, add the next file: `NNNN_short_name.sql` (the next
number, lower case and underscores), plain SQL. No `BEGIN` or `COMMIT`:
each file already runs in one transaction. Never edit a file once it has
been deployed; fix it with another.

The previous lierc-api, and the logger (lierc repo), keep running against
the new schema until they restart. So add things (tables, nullable
columns, indexes) in one deploy, and drop or rename them in a later one,
once no running code uses them. lierc-api starts against a database with
migrations it doesn't know (after an image is rolled back), logging them.

Tests
-----

```
go test -p 1 ./...
```

The tests start their own embedded PostgreSQL and nsqd; `-p 1` keeps the
packages from starting several at once.
