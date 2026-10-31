# Go stream server

`lierc-api stream` is the Server-Sent Events stream. It replaced the Perl
`bin/stream.psgi` and is the first piece of the Go rewrite: the JSON API stays
on `bin/api.psgi` for now.

It logs users in with the `chats` cookie that `bin/api.psgi` issues and reads
the same environment. It never writes a cookie: the Perl API keeps
issuing and refreshing `chats`.

## What it does

See also `docs/lierc-api.md`, "Event stream":

- `GET /stats` with a matching `lierc-key` header: stream counts per user, or
  one user's streams with `?user=<id>`. Otherwise 400 `Invalid key`.
- Any other path: 401 unless the `chats` cookie verifies and names an existing
  user. Then 2 KB of comment padding, a `stream` event with the stream's
  id (`{"id":"…"}`), a `ping`, the replay of each connection's state
  (recreating it in liercd if needed), and live messages from NSQ topic
  `logged` for the user's connections.
- Pings every 5 s. Each ping's data is `{"time":…,"last_id":…}`, where
  `last_id` is the user's newest logged message id (0 if none), so a client
  can fetch what it missed. A stream whose write blocks for 3 minutes is
  dropped. On
  close it saves each connection's joined channels into its config and updates
  `last_login`.
- On SIGTERM or SIGINT it stops accepting connections and ends every stream at
  once (a stream that starts meanwhile ends straight after its handshake), so
  clients reconnect; it then waits up to 10 s for their close work.

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
| `LISTEN` | `:5003` | |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | `X-Forwarded-For` is honoured from these peers. |
| `DB_MAX_CONNS` | `20` | Postgres pool size. |

The database name is always `lierc`; `POSTGRES_DB` is ignored.

It subscribes to NSQ on its own ephemeral channel
(`lierc-api-go-<random>#ephemeral`), so several instances each see every
message. If nsqd is down, at start or later, it keeps serving
streams (replay works; live messages wait) and retries nsqd every 5 s, not
go-nsq's default 60 s. Messages published while it is disconnected are lost,
as with any ephemeral channel.
