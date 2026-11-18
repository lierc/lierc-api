lierc-api
=========

Part of [relaychat](https://relaychat.party/), a web IRC client that stays
connected when you close the tab. Try it at <https://relaychat.party/>.

Questions or ideas? Come talk in [#relaychat](https://relaychat.party/join/?network=relaychat&channel=relaychat).

relaychat's HTTP API, in Go: accounts and login, a user's IRC connection
settings (and driving `liercd` to run them), the event stream (server-sent
events) that delivers each user's IRC traffic to the browser, history and
search, preferences, web push and email. It replaced a Perl (Plack) API,
route for route; that code is in this repository's git history, and the
JSON fixtures in `testdata/fixtures` record its behaviour, which the Go tests
check against.

The image also carries two binaries of their own: `lctl`, the admin
command (`lctl help`), and `lierc-logger`, which logs IRC traffic to
Postgres and sends the highlight notifications (web push and email). The
logger runs as its own process, from `docker run --entrypoint
/lierc-logger`; it moved here from the lierc repo, with its history.

Layout
------

- `cmd/lierc-api`: the server (`lierc-api`, no arguments).
- `cmd/lierc-logger`, `internal/logger`, `internal/notify`,
  `internal/envfile`: the logger and the notifier it runs. They read
  liercd's messages with lierc's `pkg/ircwire` (the lierc module).
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

The previous lierc-api, and the logger (its own process), keep running against
the new schema until they restart. So add things (tables, nullable
columns, indexes) in one deploy, and drop them in a later one, once no
running code uses them. lierc-api starts against a database with
migrations it doesn't know (after an image is rolled back), logging them.

A rename is not additive: it's a coordinated deploy with everything that
uses the name (the logger), and the lierc-api image can't be rolled back
past it. Stop the logger first (nsqd keeps its messages), deploy, then
start the new logger. A migration that waits more than 5 seconds for a
lock (say behind the nightly backup) is rolled back and retried for up to
a minute, so deploy migrations that lock busy tables outside the backup
and nightly window (07:00-09:30 UTC).

Every row references the user or connection it belongs to, with
`ON DELETE CASCADE`: deleting a user or connection deletes everything of
theirs in the same statement, and nothing is left for a later clean-up. A
new table that belongs to a user or connection gets such a key too, and
an index leading with that column so the cascade doesn't scan the table.

Tests
-----

```
go test -p 1 ./...
```

The tests start their own embedded PostgreSQL and nsqd; `-p 1` keeps the
packages from starting several at once.
