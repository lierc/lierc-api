# Go stream server

`lierc-api stream` is a Go replacement for `bin/stream.psgi`, the
Server-Sent Events stream. It is the first piece of the Go rewrite: the JSON
API stays on `bin/api.psgi` for now.

It is a drop-in replacement. It serves the same paths with the same
responses, logs users in with the `chats` cookie that `bin/api.psgi` issues,
and reads the same environment. It never writes a cookie: the Perl API keeps
issuing and refreshing `chats`.

## What it does

The same as `bin/stream.psgi` (see `docs/lierc-api.md`, "Event stream"):

- `GET /stats` with a matching `lierc-key` header: stream counts per user, or
  one user's streams with `?user=<id>`. Otherwise 400 `Invalid key`.
- Any other path: 401 unless the `chats` cookie verifies and names an existing
  user. Then 2 KB of comment padding, a `ping`, the replay of each
  connection's state (recreating it in liercd if needed), and live messages
  from NSQ topic `logged` for the user's connections.
- Pings every 15 s; a stream whose write blocks for 3 minutes is dropped. On
  close it saves each connection's joined channels into its config and updates
  `last_login`.

## Running it

    lierc-api stream

or the image `ghcr.io/lierc/lierc-api-go` (built from `go.Dockerfile`; the
default command is `stream`).

| Variable | Default | Notes |
|----------|---------|-------|
| `API_SECRET` | `changeme` | Must match `bin/api.psgi`, or no cookie verifies. |
| `API_KEY` | `changeme` | The `/stats` key. |
| `LIERCD_HOST` | `127.0.0.1` | liercd at `http://<host>:5005/`. |
| `POSTGRES_HOST` | `127.0.0.1` | May carry a port (`db:5433`). |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | empty | |
| `NSQD_HOST` | `127.0.0.1` | nsqd TCP; port 4150 unless given (`nsq:4160`). |
| `LISTEN` | `:5003` | Go only. |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | Go only. `X-Forwarded-For` is honoured from these peers. |
| `DB_MAX_CONNS` | `20` | Go only. Postgres pool size. |

As in Perl, the database name is always `lierc`; `POSTGRES_DB` is ignored.

It subscribes to NSQ on its own ephemeral channel
(`lierc-api-go-<random>#ephemeral`), so it can run next to the Perl stream and
both see every message. If nsqd is down, at start or later, it keeps serving
streams (replay works; live messages wait) and retries nsqd every 5 s, not
go-nsq's default 60 s. Messages published while it is disconnected are lost,
as with any ephemeral channel.

## Cutting over

Point nginx's stream location at the Go server in place of the Perl one;
clients need no change. Before that, the parity suite (`parity/README.md`)
must pass in CI.
