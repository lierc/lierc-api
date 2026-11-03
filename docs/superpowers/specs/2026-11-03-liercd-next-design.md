# A second liercd for trying the irc-go branch

## Why

liercd's move onto irc-go (lierc branch `irc-go`) changes how every IRC
connection runs. Restarting liercd drops everyone's connections, and a
deploy from `master` would replace it for everyone at once. A second
liercd instance running the branch lets individual connections be moved
onto it, tried for real, and moved back, while everyone else stays on the
current liercd.

## Goals

- A second liercd, `next`, running the `irc-go` image beside the current
  one (`main`), on the same host.
- Any single connection can be moved between `main` and `next` with one
  command, and runs on exactly one of them at a time.
- Everything that talks to liercd (the API, the stream server, `lctl`)
  reaches the instance the connection is on.
- With no connection moved, behaviour is exactly as today.

## Non-goals

- Moving whole users at once (decided by the user: per connection).
- Ident for connections on `next` (decided by the user): identd asks only
  `main` for its port map, and only one process can bind port 113, so
  `next` connections get no ident reply (a `~` username on networks that
  check). It goes away when the branch is promoted.
- A UI for choosing the instance. Moves are done by an operator.
- More than two instances.

## Anti-goals

- A connection running on both instances at once: the user would appear
  twice on IRC and the nicks would collide.
- A call for a `next` connection silently reaching `main` (or the reverse)
  because routing failed.

## Approaches considered

- **Per connection (chosen):** a column on `connection`. Every liercd call
  is already keyed by connection id (`/<id>/create`, `/<id>/raw`, ...), so
  routing is one lookup by that id.
- **Per user:** a column on `user`; all of a user's networks move together.
  Coarser than wanted.
- **A routing proxy in front of both liercds:** no lierc-api change, but
  another service to run, and it still needs the same mapping.

## Design

### Data

`connection.liercd text`, NULL for `main`, `'next'` for the new instance.
New connections (including signup's default network) are created with
NULL, so they start on `main`; only `lctl move-connection` changes it.

Production is altered by hand before lierc-api deploys
(`ALTER TABLE connection ADD COLUMN liercd text`), then `lierc.sql` gets
the column, as with earlier schema changes.

### Routing in lierc-api

All 18 calls to liercd go through two functions: Perl's
`API::Liercd::request` (Connection.pm, Auth.pm, `find_or_recreate_connection`)
and Go's `liercd.Client.Do` (the stream server's replay and recreate, and
every `lctl` command). Both:

1. take the connection id from the start of the path (`<id>/...`),
2. look up that connection's `liercd` column,
3. send the request to that instance's host: `LIERCD_HOST` for NULL
   (as today), `LIERCD_NEXT_HOST` for `next`.

A connection whose instance has no configured host (`next` with
`LIERCD_NEXT_HOST` unset) fails with an error naming the instance; it
never falls back to the other instance. A missing row (a connection being
created, before its row exists, or one already deleted) routes to `main`,
as today. The port stays 5005 on both instances.

The lookup is one primary-key query per liercd call, including each
message sent. That's acceptable at relaychat's volume; caching is left
out (YAGNI) unless it shows up.

### Moving a connection

`lctl move-connection <id> main|next`:

1. refuses an unknown instance, an unknown connection, a move to the
   instance it's already on, or `next` when `LIERCD_NEXT_HOST` is unset;
2. destroys the connection on its current instance (404 is fine: it may
   not be running);
3. sets the column;
4. creates it on the new instance from its saved config.

If step 4 fails, the column is already set, so the stream server's
recreate-on-open (or `lctl reconnect`) creates it on the new instance
later; the command says so. The user sees a reconnect, with the usual
DISCONNECT and CREATE events in the UI and the log.

### relaychat-infra

- `images.lierc_next`: the `irc-go` image (`ghcr.io/lierc/lierc:sha-...`).
- A `liercd-next` unit, a copy of `liercd`'s with `--name` from the unit,
  `-e LISTEN=127.0.0.2:5005` and `images.lierc_next`, and
  `manual_restart: true`. `127.0.0.0/8` is all loopback on Linux, so no
  port changes anywhere.
- `LIERCD_NEXT_HOST=127.0.0.2` in `lierc.env`, which lierc-api,
  lierc-api-stream and `lctl` already read.
- The logger and notifier need nothing: both liercds publish to the same
  NSQ topics.

### lierc CI

The image workflow also builds pushes to `irc-go`, tagged `sha-<short>`
only; `latest` stays on `master`. The `irc-go` branch is pushed to GitHub
for this.

## Rollout

1. Alter the production table.
2. Merge and deploy lierc-api (a no-op until a connection moves).
3. Push lierc's CI change to `master` and the `irc-go` branch; wait for
   its image.
4. Deploy relaychat-infra with `liercd-next`.
5. Move one of the user's own connections first with `lctl`.

Promoting the branch later: deploy it as `main`, move every `next`
connection back, then remove `liercd-next`.

## Testing

- Perl: `request` sends a `next` connection's call to `LIERCD_NEXT_HOST`,
  a NULL or missing one to `LIERCD_HOST`, and errors for `next` without
  `LIERCD_NEXT_HOST`.
- Go: the same for `Client.Do`, and `lctl move-connection`'s order
  (destroy on the old instance, column, create on the new), its refusals,
  and a failed create leaving the column set.
- End to end in the dev stack with two liercds: move a connection to
  `next`, send a message, see it arrive; move it back.

## Left to the builder

- How the Go client gets its lookup (a function from id to host, given to
  `liercd.New`, is the expected shape).
- `lctl` usage text and output wording.
