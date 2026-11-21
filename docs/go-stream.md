# Go stream server

The Server-Sent Events stream, part of `lierc api`. It replaced the Perl
`bin/stream.psgi` and is the first piece of the Go rewrite: the JSON API stays
on `bin/api.psgi` for now.

It logs users in with the `chats` cookie that `bin/api.psgi` issues and reads
the same environment. It never writes a cookie: the Perl API keeps
issuing and refreshing `chats`.

## What it does

See also `docs/lierc-api.md`, "Event stream":

- On the admin listener (`ADMIN_LISTEN`) only, `GET /stats`: stream counts
  per user, or one user's streams with `?user=<id>`. It has no
  authentication, so the admin listener must stay on loopback and must not
  be proxied by nginx. `lctl stats`/`users` and lierc's notifier use it
  (`API_STATS`).
- Any path on the main listener: 401 unless the `chats` cookie verifies and names an existing
  user. Then 2 KB of comment padding, a `stream` event with the stream's
  id (`{"id":"…"}`), a `ping`, the replay of each connection's state
  (recreating it in liercd if needed), and live messages for the user's
  connections, which the logger sends as Postgres NOTIFYs on `logged`.
- Pings every 5 s. Each ping's data is `{"time":…,"last_id":…}`, where
  `last_id` is the user's newest logged message id (0 if none), so a client
  can fetch what it missed. A stream whose write blocks for 3 minutes is
  dropped. On close it saves each connection's joined channels into its
  config and updates `last_login`.
- `POST /<stream id>/ping` (leading slashes may repeat): 204, and a ping is
  sent at once on that stream of the logged-in user, so a client waking
  from sleep can check its stream. 404 `unknown stream` if the user has no
  such stream, 401 without a valid session.
- On SIGTERM or SIGINT it stops accepting connections and ends every stream at
  once (a stream that starts meanwhile ends straight after its handshake), so
  clients reconnect; it then waits up to 10 s for their close work.

## Running it

    lierc api

or the image `ghcr.io/lierc/lierc-api` (built from `go.Dockerfile`; the
default command is `api`).

| Variable | Default | Notes |
|----------|---------|-------|
| `API_SECRET` | `changeme` | Must match `bin/api.psgi`, or no cookie verifies. |
| `LIERCD_HOST` | `127.0.0.1` | liercd at `http://<host>:5005/`. |
| `POSTGRES_HOST` | `127.0.0.1` | May carry a port (`db:5433`). |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | empty | |
| `LISTEN` | `:5003` | |
| `ADMIN_LISTEN` | `127.0.0.1:5009` | The admin listener (`/stats`). Keep it on loopback. |
| `TRUSTED_PROXIES` | `127.0.0.1,::1` | `X-Forwarded-For` is honoured from these peers. |
| `DB_MAX_CONNS` | `20` | Postgres pool size. |

The database name is always `lierc`; `POSTGRES_DB` is ignored.

It doesn't use NSQ. The logger sends it each message it handled as a
Postgres NOTIFY on `logged`, which it LISTENs for on one connection of its
own (`lierc stream listener` in `pg_stat_activity`), so several instances
each get every one. liercd's `000` status line comes with a `Status` field,
set by liercd in its message envelope (never from an IRC server's line),
and goes out as `connection:connect` or `:disconnect` too.
If that connection drops it keeps serving streams (replay works; live
messages wait) and reconnects every 5 s. Notifications sent while it is
disconnected are lost; clients catch up from `last_id`.
