# Connection events

## Intent

Make adding, editing, removing, connecting and disconnecting a network
reliable in the UI. These changes reach the UI today as fake IRC messages
(`CREATE`, `DELETE`, `CONNECT`, `DISCONNECT`) that travel liercd → NSQ
`chats` → logger → NSQ `logged` → stream server → UI.

Problems today:

- **Removing a connection usually leaves its panels until a reload.** The
  API asks liercd to destroy the connection, which emits `DELETE`, then
  deletes the row straight away. By the time the `DELETE` reaches the
  stream server, `Deliver`'s `LookupOwner` finds no row and drops it
  (`internal/stream/server.go:227`).
- **A `DELETE` missed for any reason is never made up.** A stream that
  reconnects is only sent `CREATE` for the connections that still exist,
  so a stale network stays until the page is reloaded.
- **Editing with Save & Reconnect throws the network's panels away.**
  liercd's `DELETE` and `CREATE` remove every panel. Channels come back as
  they rejoin, losing scroll position and typed text, and DM panels don't
  come back. Plain Save renames the panel only in the tab it was made
  from.
- **The UI picks connection state out of the IRC message stream** by
  command name.

## Decisions (the user's)

- **Approach B:** the API announces connection changes itself through
  Postgres, and the stream server sends dedicated SSE events. liercd stops
  emitting the fake `CREATE` and `DELETE`.
- **Edits keep panels in place.** Save & Reconnect sends
  `connection:update`. The panels stay put, show disconnected and then
  reconnected, and the name updates in place in every open tab.
- **Message inputs stay enabled** while a network is disconnected, as
  today.
- **liercd separates connection status into a log line and a state
  message** (replacing the earlier "translate `CONNECT`/`DISCONNECT` at
  the stream server" decision). On each status change liercd emits:
  - a `000` numeric with the status text, stored by the logger in the
    status history and shown there;
  - plain JSON state on a new NSQ topic, `status`, which the stream server
    consumes directly and sends as `connection:connect` or
    `connection:disconnect`.
- **NSQ stays for now.** The user plans to replace it separately.

## Approaches considered

- **A.** The stream server falls back to an in-memory map from connection
  to owner when `LookupOwner` misses. A small fix, but the fake events
  stay and the missed-event gap remains.
- **B (chosen).** The API notifies through Postgres, with dedicated SSE
  events and a snapshot of every connection when a stream opens. It fixes
  the root cause and closes the gap.
- **C.** Delete the row later, or mark it deleted. Rejected: it only
  narrows the race window, and everything reading the table would need to
  skip deleted rows.

## Design

### Events (stream server → UI)

| SSE event | When | `data` (JSON) |
|---|---|---|
| `connections` | first, when a stream opens (start of the replay) | `[{"Id","Name","Nick","Connected"}, ...]`, every connection the user has |
| `connection:create` | the API created a connection | `{"Id","Name","Nick"}` |
| `connection:update` | the API saved a connection (Save, or Save & Reconnect) | `{"Id","Name"}` |
| `connection:delete` | a connection was deleted (by the API or lctl) | `{"Id"}` |
| `connection:connect` | a `status` message with `Connected: true` | `{"Id"}` |
| `connection:disconnect` | a `status` message with `Connected: false` | `{"Id"}` |

- **`Name`** is the connection's `Alias` if it has one, otherwise its
  `Host`, matching what the replay shows today. **`Nick`** is the config's
  `Nick`. Both come from the row when the stream server sends the event.
  **`update` carries no nick** (my call): the nick a connection is
  actually using can differ from the configured one after a nick clash,
  and the UI keeps tracking it from IRC `NICK` events.
- **`connect` and `disconnect` carry state only**, with no SSE id: they
  aren't stored messages. The line for the status panel arrives
  separately as the `000` message.

### API and lctl → stream server, through Postgres

- **The notification:** `SELECT pg_notify('connections', <json>)`, where
  `<json>` is `{"user": "<user id>", "connection": "<id>", "action":
  "create"|"update"|"delete"}`. It carries ids only, well under
  `NOTIFY`'s 8000-byte limit. The stream server reads the row for the
  name and nick.
- **Perl API** (`lib/API/Controller/Connection.pm`):
  - `start` sends `create` after the row is saved and liercd has created
    the connection, whether or not liercd succeeded, since the row exists
    either way.
  - `delete` sends `delete` after `delete_connection`.
  - `edit` sends `update` on both of its paths: the save-only path and the
    destroy-and-recreate path.
  - `save` (`PUT /connection/:id/config`) sends `update`.
- **lctl** (`internal/lctl`): `delete-connections`, `delete-users` and
  `check-connections --delete` send `delete` for each connection they
  remove, with its owner, after the delete commits.
- **The stream server** opens a dedicated pgx connection and runs `LISTEN
  connections`. It reconnects with the same backoff the NSQ consumer
  uses. Notifications sent while it's down are lost; the next
  `connections` snapshot repairs the UI.
- **On a notification** the stream server finds that user's open streams
  and sends them the event. For `create` and `update` it reads the row
  (`LookupConnection(id)` returning id, owner, alias, host and nick),
  skipping the event if the row is gone.

### Stream server: delivering liercd's messages

- **`Deliver`:** a message whose command is `CREATE` or `DELETE` is
  dropped, not sent. Only liercd makes these; no IRC server sends
  commands with those names. They stay dropped while an old liercd image
  still emits them, since liercd-next is restarted only by hand.
  `CONNECT` and `DISCONNECT` from an old liercd pass through unchanged as
  `irc` events, and the UI keeps handling them.
- **The `status` topic:** a second NSQ consumer on its own ephemeral
  channel, `lierc-api-go-<random>#ephemeral`, set up like `logged`. It
  decodes `{"Id","Connected"}`, looks up the owner with `LookupOwner` (the
  row exists while a connection runs), and sends `connection:connect` or
  `connection:disconnect` `{"Id"}` to the owner's streams. An unknown
  connection is skipped.
- **The replay** (`internal/stream/replay.go`) first sends `connections`,
  built from the user's rows plus each status's `Connected`. It no longer
  sends `CREATE` or the parameterless `CONNECT`. The welcome, CAP, JOIN,
  topic and names replay are unchanged. If a replay fails today, nothing
  is sent, and that stays true.

### liercd and the logger (lierc)

- **Stop emitting** the fake `CREATE` (`manager.go:149`) and `DELETE`
  (`manager.go:164`) on create, destroy and revive.
- **`Status`** (`manager.go:46`, currently `ConnectEvent`) emits two
  things for each status change, in place of `CONNECT`/`DISCONNECT`:
  - **On `chats`:** `:<host> 000 <nick> :<status text>`, where `<nick>`
    is the connection's current nick, or `*` before it's registered. The
    UI shows numerics without their first parameter, so this displays as
    the status text.
  - **On the new `status` topic:** `{"Id","Connected","Message","Time"}`,
    the existing `lierc.IRCClientStatus` fields plus `Time`.
- **The logger** classifies `000` as a status message (`classify.go`
  `logType`), so it's stored under the status panel like `CONNECT` and
  `DISCONNECT` are today. Old `CONNECT` and `DISCONNECT` rows in history
  are unchanged and still render.

### UI (lierc-minimal-ui)

- **`stream.js`** listens for the six new event types and fires matching
  app events. It keeps handling `irc` `CREATE`, `DELETE`, `CONNECT` and
  `DISCONNECT` too, so it works against the old stream server during the
  rollout.
- **`connections`:** call `setup_connection` for each id it lacks, remove
  every panel of a connection not in the list, and set each connection's
  connected state.
- **`connection:create`:** `setup_connection(Id, Name, Nick)`.
- **`connection:update`:** rename the status panel; the nick is left to
  IRC `NICK` events. No panels are removed. The edit dialog's local rename stays and is
  harmless.
- **`connection:delete`:** today's `delete_connection` handler (remove the
  panels, forget the connection).
- **`connection:connect` and `connection:disconnect`:** call
  `set_connected(Id, true or false)`. The `000` line reaches the status
  panel through the normal message path, as an unknown numeric, with no
  UI change. `irc` `CONNECT` and `DISCONNECT` keep working for an old
  liercd.

### Rollout order

The UI is deployed first, because it handles both the old and the new
events. Then the API, lctl and the stream server go out together; the
stream server's `status` consumer has nothing to read until liercd
changes. Then lierc: the logger, which stores `000`, restarts on deploy.
liercd's change takes effect when liercd-next is next restarted by hand,
so the logger is always ready first. The stream server drops stray `CREATE` and `DELETE` meanwhile. A tab
still running the old UI after the stream server changes gets events it
ignores, so it can go stale until it's reloaded. `make bust` and the
Fastly purge make any reload pick up the new UI.

## Behaviour changes to expect

- **Removing a network** removes its panels at once, in every tab.
- **Save & Reconnect** keeps the panels: they show disconnected, then
  connected, and are renamed.
- **lctl `stop-inactive` and `move-connections`** destroy and recreate
  connections in liercd without touching rows. Today their `DELETE`
  removes the panels. Afterwards the panels stay, showing disconnected
  (stop) or reconnecting (move).

## Non-goals

Replacing NSQ (the new `status` topic moves with the rest when it is).
Changing how messages, NICK and QUIT are stored. Disabling
inputs while disconnected. Recovering events lost while the stream
server's `LISTEN` connection was down, other than through the next
snapshot.

## Left to the builder

- **Code structure:** how `LISTEN` and its reconnects are laid out in
  `internal/stream`.
- **Test style:** the stream server's tests already use a real Postgres
  (`testenv`), and the UI is checked with throwaway Node and Chromium
  scripts.
- **Wording** of the log lines.
