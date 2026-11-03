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
as today. A host is liercd's port 5005 unless it gives its own
(`127.0.0.1:5008`).

The lookup is one primary-key query per liercd call, including each
message sent. That's acceptable at relaychat's volume; caching is left
out (YAGNI) unless it shows up.

### Moving a connection

`lctl move-connection <id> main|next`:

1. refuses an unknown instance, an unknown connection, a move to the
   instance it's already on, or `next` when `LIERCD_NEXT_HOST` is unset;
2. asks the new instance for the connection's status, by host, and
   refuses if that instance can't be reached or is already running it;
3. destroys the connection on its current instance (404 is fine: it may
   not be running);
4. sets the column;
5. destroys it on the old instance again, by host (404 is fine). A user's
   stream opening between steps 3 and 4 finds it missing and recreates it
   through the column, which still names the old instance; this removes
   that copy, and the command says it did;
6. creates it on the new instance from its saved config.

Steps 2, 5 and 6 go to an instance by its host rather than through the
column, so the column changing under them can't send them to the wrong one.

If step 6 fails, the column is already set, so the stream server's
recreate-on-open (or `lctl reconnect`) creates it on the new instance
later; the command says so. The user sees a reconnect, with the usual
DISCONNECT and CREATE events in the UI and the log.

### relaychat-infra

- `images.lierc_next`: the `irc-go` image (`ghcr.io/lierc/lierc:sha-...`).
- A `liercd-next` unit, a copy of `liercd`'s with `--name` from the unit,
  `-e LISTEN=127.0.0.1:5008` and `images.lierc_next`, and
  `manual_restart: true`. (The first plan, `127.0.0.2:5005`, didn't work:
  production's liercd was started from an image older than its `LISTEN`
  support and binds `*:5005`, which covers every loopback address; a
  separate port works whatever main binds.)
- `LIERCD_NEXT_HOST=127.0.0.1:5008` in `lierc.env`, which lierc-api,
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

Promoting the branch later: see "Operating it".

## Operating it

- **Starting `liercd-next`.** It is `manual_restart`, so the playbook
  installs it but doesn't start or restart it. After a deploy, start it by
  hand (`systemctl start liercd-next`) and check it answers:
  `curl -s 127.0.0.1:5008/portmap` should print `[]` (main's port map
  means liercd-next didn't get the port and something else answered).
- **Which connections are on `next`:**
  `SELECT id FROM connection WHERE liercd IS NOT NULL`.
- **Before rolling lierc-api back** (either image, `lierc_api` or
  `lierc_api_go`) past this change, **or stopping or removing
  `liercd-next`**, move every one of those rows back to `main` with
  `lctl move-connection ID main`. Older images route every call to `main`,
  and the stream server recreates any connection `main` isn't running, so a
  `next` connection would then run on both instances.
- **If `liercd-next` is down**, a moved user's stream replay fails, and
  replay is all-or-nothing, so the user gets none of their connections, not
  only the moved one. Restart `liercd-next`, or move their `next`
  connections back to `main`.
- **Promoting the branch:**
  1. pin the `irc-go` image as `images.lierc`, deploy, and restart
     `liercd` by hand (it is `manual_restart` too);
  2. move every `next` row back to `main` (the query above);
  3. `systemctl disable --now liercd-next`;
  4. remove the `liercd-next` unit and its services entry,
     `images.lierc_next`, and `LIERCD_NEXT_HOST` from `lierc.env`, and
     deploy.
- **Removing it without promoting:** steps 2 to 4.

## Testing

- Perl: `request` sends a `next` connection's call to `LIERCD_NEXT_HOST`,
  a NULL or missing one to `LIERCD_HOST`, and errors for `next` without
  `LIERCD_NEXT_HOST`.
- Go: the same for `Client.Do`, and `lctl move-connection`'s order
  (status on the new instance, destroy on the old, column, destroy on the
  old again, create on the new), its refusals (including the new instance
  unreachable or already running it), a recreate slipping in mid-move, and
  a failed create leaving the column set.
- End to end in the dev stack with two liercds: move a connection to
  `next`, send a message, see it arrive; move it back.

## Left to the builder

- How the Go client gets its lookup (a function from id to host, given to
  `liercd.New`, is the expected shape).
- `lctl` usage text and output wording.
