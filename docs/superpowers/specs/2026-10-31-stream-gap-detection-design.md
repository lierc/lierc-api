# Stream gap detection and wake pings

Spans lierc-api (Go stream server, `internal/stream`) and lierc-minimal-ui
(`static/js/lierc/stream.js`, `app.js`).

## Why

The UI sometimes looks connected and up to date while missing messages that
show up on reload: they were logged but never reached the page. The cause is
the client's reconnect logic:

- `Stream.last_id` is set only from live stream events, so after a page load
  on quiet channels it is `null` and a reconnect skips `fill_missed`.
- The `open` handler only calls `fill_missed` when a panel is focused.

Separately, after sleep the page waits up to 7 seconds (`wake_timeout`) for a
ping before deciding its stream is dead, even when the server dropped it long
ago.

## Goals

- The page notices and fetches any gap between what it has seen and what the
  server has, while connected, including in background tabs.
- After wake, a dead stream is detected in one round trip.

Non-goals: server-side timeouts for silent clients; detecting messages lost
before the stream server's `Deliver` (e.g. while NSQ is disconnected);
replacing `/auth`, which stays as is (it also refreshes the session cookie,
which the Go server never writes).

## Design

### Server pings carry the newest message ID

- The stream server keeps the newest message ID per user in memory.
  `Deliver` raises it (max, never lowers) for every message with a
  `MessageId`.
- When a user's first stream opens, it is seeded from Postgres
  (`SELECT max(id) FROM log WHERE "user"=$1`), taking the max with any value
  `Deliver` set meanwhile. The entry is dropped when the user's last stream
  closes, since `Deliver` skips users with no streams.
- Every ping (the 5-second ones and the one in the handshake) carries that ID
  and the time. The ping data changes from the bare time to JSON; current
  clients ignore ping data, so this is compatible.

### Streams announce their ID

The handshake (padding plus first ping) also sends the stream's ID, so the
client can name its stream. Old clients ignore event types they don't
listen for.

### Wake pings (approach A)

- `POST` to the stream server for `/<stream id>/ping` (reached as
  `/api/events/<stream id>/ping` through nginx and Fastly).
- The server checks the session's user owns that stream. Unknown stream, or
  someone else's: 404. Otherwise it queues a ping on that stream at once and
  returns 2xx.
- The client sends it when the tab becomes visible or on `pageshow`,
  replacing `wake()`'s 7-second check. On 404 or a network error it
  reconnects at once; if no ping arrives within 5 seconds, it reconnects.

### Client gap check

- On every ping: if the ping's ID is ahead of `last_id`, call
  `fill_missed(last_id)`. At most one fill runs at a time; a ping during a
  fill is checked again after it.
- `last_id` only moves forward (max), and `fill_missed`'s results advance it.
- If `last_id` is `null` when a ping arrives (fresh page), the ping's ID
  becomes the baseline, with nothing to fetch.
- After every reconnect, `fill_missed` runs when `last_id` is set, focused
  panel or not.

## Approaches considered

- **A, ping a specific stream (chosen):** 404 tells the client at once that
  its stream is gone, which is the point of a wake ping.
- **B, ping all of the user's streams by cookie:** no stream ID needed, but
  the client always waits out the timeout, and other tabs get extra pings.
- **Client pings every 5 seconds replacing server pings:** rejected. Hidden
  tabs throttle timers to once a minute, and Fastly's 30-second
  `between_bytes_timeout` would cut those streams.
- **Pong only on client pings, no ID on server pings:** rejected in favour
  of the check running on every server ping, with no client timers needed.

## Calls made in writing this up

- `last_id` gets its baseline from the first ping, not from backlog loads.
  A channel's backlog says nothing about the newest message overall.
- The wake ping times out after 5 seconds.
- The ping data is JSON, e.g. `{"time": 1761900000.123, "last_id": 12345}`;
  the stream ID event's name and shape are left to the builder.

## Left to the builder

- nginx's `location /api/events` rewrites the prefix to `/`, so the ping
  path arrives at the Go server with that prefix stripped; route
  accordingly, keeping `/stats` and the stream on every other path working.
- The `fill_missed` error path (`reset()` on "Too many messages!") is
  unchanged.
