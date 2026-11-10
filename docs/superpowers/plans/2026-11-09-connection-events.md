# Connection Events Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connection add, edit, delete, connect and disconnect reach the UI as dedicated SSE events, announced by the API and lctl through Postgres `NOTIFY`, with a snapshot of every connection when a stream opens. liercd's fake `CREATE` and `DELETE` are retired.

**Architecture:** The Perl API and lctl run `pg_notify('connections', json)`. The Go stream server `LISTEN`s and sends `connection:create`, `connection:update` and `connection:delete` to the owner's streams. Its `Deliver` drops `CREATE`/`DELETE`. A new NSQ `status` topic from liercd becomes `connection:connect`/`connection:disconnect`. Its replay opens with a `connections` snapshot. liercd emits each status change as a `000` log line (stored by the logger) plus a JSON state message, and stops emitting `CREATE`/`DELETE`/`CONNECT`/`DISCONNECT`. The UI handles both the old and new events and is deployed first.

**Tech Stack:** Go (pgx v5, lierc-api `internal/stream`, `internal/db`, `internal/lctl`), Perl (Plack API, DBI), vanilla JS UI, Ansible.

**Spec:** `lierc-api/docs/superpowers/specs/2026-11-09-connection-events-design.md`

## Global Constraints

- **The Postgres channel** is `connections`. The payload is the JSON
  `{"user":"<id>","connection":"<id>","action":"create"|"update"|"delete"}`.
- **SSE event names** are exactly `connections`, `connection:create`,
  `connection:update`, `connection:delete`, `connection:connect` and
  `connection:disconnect`.
- **Payloads:**
  - `connections` is `[{"Id","Name","Nick","Connected"}]`.
  - `connection:create` is `{"Id","Name","Nick"}`.
  - `connection:update` is `{"Id","Name"}`.
  - `connection:delete` is `{"Id"}`.
  - `connection:connect` and `connection:disconnect` are `{"Id"}`, with no
    SSE id.
- **liercd's `status` topic** carries `{"Id","Connected","Message","Time"}`.
  The status line is `:<host> 000 <nick or *> :<status text>` on `chats`.
- **`Name`** is the config's `Alias` if non-empty, otherwise its `Host`.
  **`Nick`** is the config's `Nick` for `create`. In the snapshot, `Nick`
  and `Connected` come from liercd's status, as the replay uses today.
- **`Deliver` drops** any message whose command is `CREATE` or `DELETE`.
  `CONNECT`/`DISCONNECT` from an old liercd pass through unchanged as
  `irc`.
- **The UI keeps handling** `irc` `CREATE`, `DELETE`, `CONNECT` and
  `DISCONNECT` throughout. Inputs stay enabled while disconnected.
- **Rollout order:**
  1. the UI, from its branch;
  2. lierc-api, with the `lierc_api`, `lierc_api_go` and `lctl` pins
     together;
  3. lierc.

  liercd-next picks up step 3 only on its manual restart.
- **Branches and pushes:**
  - UI work goes on branch `connection-events` and is deployed from it;
    `master` is fast-forwarded only after the user approves.
  - lierc-api and lierc are committed on `master`, and infra on `main`.
  - Push and deploy only with the user's go-ahead.

## Review Focus

1. **Deleting a connection with the stream server's `LISTEN` down or
   restarting.** The UI must lose the panels at the latest on its next
   stream open, through the snapshot. Task 1's harness: `connections`
   removes a stale connection's panels. Task 3: `TestReplaySnapshot` and
   `TestListenReconnects`.
2. **A `pg_notify` inside a transaction that rolls back** must send
   nothing. Postgres guarantees this. Task 2:
   `TestDeleteRowsNotifyOnlyOnCommit`.
3. **A notification for a user with no open streams**, or for a row that's
   already gone on `create`/`update`, is ignored without error or log
   spam. Task 3: `TestConnectionNotifyNoStreams` and
   `TestConnectionNotifyRowGone`.
4. **A malformed notification payload** is logged and skipped, and the
   listener keeps running. Task 3: `TestConnectionNotifyMalformed`.
5. **An old liercd still sending `CREATE`/`DELETE`** after the stream
   server is deployed: they never reach the UI. Task 3:
   `TestDeliverDropsCreateDelete`.

---

### Task 1: UI handles the new events (lierc-minimal-ui, branch `connection-events`)

**Files:**
- Modify: `static/js/lierc/stream.js`: in `connect()`, add listeners for
  the six events. `connections`, `connection:create`, `connection:update`
  and `connection:delete` fire app events of the same names with the
  parsed `data`. `connection:connect` and `connection:disconnect`
  fire app events too, after `touch()`. Remove them in `close()` alongside `irc`.
- Modify: `static/js/lierc/app.js`: in `connect()`:
  - **`connections`:** `setup_connection(Id, Name, Nick)` for each id not
    in `this.connections`. Remove every panel whose `connection` isn't in
    the list, and `delete this.connections[id]` for it. Call
    `set_connected(Id, Connected)`.
  - **`connection:create`:** `setup_connection(Id, Name, Nick)`.
  - **`connection:update`:** `rename_connection(Id, Name)`.
  - **`connection:delete`:** the existing `delete_connection` handler's
    body. Factor it into `remove_connection(id)` and use it from both.
  - **`connection:connect` and `connection:disconnect`:**
    `set_connected(Id, true or false)`. The `000` line needs no UI change:
    it's an unknown numeric, shown in the status panel without its first
    parameter.
- Test: a throwaway Node harness outside the repo, plus headless Chromium
  against the dev stack after Task 3.

- [ ] **Step 1: Write the failing harness.** Load `stream.js` with a fake
  `EventSource`. Dispatch each new event and assert the app event fired. Load
  `app.js`'s handlers against a stub app and assert:
  - `connections` adds a missing connection and removes a stale one's
    panels;
  - `connection:update` renames without removing panels;
  - `connection:delete` removes the panels;
  - `connection:disconnect`/`connect` toggle `set_connected`;
  - a `000` message renders as its text in the status panel.
- [ ] **Step 2: Run it and watch it fail.** Run
  `node <harness> lierc-minimal-ui`. Expected: FAIL, because there are no
  listeners.
- [ ] **Step 3: Implement** in `stream.js` and `app.js`.
- [ ] **Step 4: Run the harness again.** Expected: PASS. Also check that
  the old `irc` `CREATE`/`DELETE` path still works in the harness.
- [ ] **Step 5: Commit** with "Handle connection events from the stream
  server alongside the old CREATE/DELETE". Then run `make bust` and commit
  "make bust".

### Task 2: lierc-api database and lctl send `pg_notify`

**Files:**
- Modify: `internal/db/db.go`. Add
  `func (d *DB) LookupConnection(ctx context.Context, id string) (owner string, cfg json.RawMessage, ok bool, err error)`.
  It's `SELECT "user", config FROM connection WHERE id=$1`, with
  `ok == false` when there's no row.
- Create: `internal/db/notify.go`. Add `const ConnectionsChannel =
  "connections"`, the type
  `type ConnectionChange struct { User string `json:"user"`; Connection string `json:"connection"`; Action string `json:"action"` }`,
  and the constant `NotifyDeleteSQL`:
  `SELECT pg_notify('connections', json_build_object('user', "user", 'connection', id, 'action', 'delete')::text) FROM connection WHERE <where>`.
  The where clause is supplied by the caller, `id=$1` or `"user"=$1`.
- Modify: `internal/lctl/check_connections.go` `deleteRows`. Inside the
  transaction, before the deletes, run the notify for `id=$1`.
- Modify: `internal/lctl/delete_users.go` `deleteUser`. Inside the
  transaction, before the deletes, run it for `"user"=$1`.
- Test: `internal/db/notify_test.go` and `internal/lctl` tests, using
  `testenv`'s Postgres.

- [ ] **Step 1: Write the failing tests.**
  - **`TestLookupConnection`:** returns owner and config, and `ok=false`
    for an unknown id.
  - **`TestDeleteRowsNotifies`:** `LISTEN connections` on a separate
    connection, run `deleteRows` for a seeded connection, and receive
    `{"user":"u1","connection":"c1","action":"delete"}` within 2s.
  - **`TestDeleteRowsNotifyOnlyOnCommit`:** run the notify in a
    transaction that's rolled back; nothing is received within 300ms.
  - **`TestDeleteUserNotifiesEachConnection`:** one notification per
    connection of the user.
- [ ] **Step 2: Run them and watch them fail.** Run
  `go test ./internal/db/ ./internal/lctl/ -run 'LookupConnection|Notif'`.
  Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them again.** Expected: PASS, with
  `go test -race ./...` green.
- [ ] **Step 5: Commit** with "db, lctl: LookupConnection; deletes notify
  the connections channel".

### Task 3: Stream server: LISTEN, the new events, Deliver and the replay snapshot

**Files:**
- Create: `internal/stream/connections.go`:
  - `func (s *Server) ListenConnections(ctx context.Context, d *db.DB)`
    acquires a dedicated `*pgxpool.Conn`, runs `LISTEN connections`, and
    loops on `WaitForNotification`. When the connection is lost it
    releases it and retries every `s.NSQReconnect`. It returns when `ctx`
    is done.
  - `func (s *Server) connectionChanged(ctx context.Context, payload string)`
    decodes a `db.ConnectionChange` and sends the event to
    `s.streams[User]` with `offerLocked`:
    - `create` and `update` call `LookupConnection` and skip the event
      when `!ok`;
    - `delete` sends `{"Id"}` with no lookup;
    - an unknown action or bad JSON is logged once and skipped.
- Modify: `internal/stream/server.go` `Deliver`: drop `CREATE` and
  `DELETE`, decoding `Message.Command` with the existing unmarshal.
  Everything else, including an old liercd's `CONNECT`/`DISCONNECT`, is
  unchanged.
- Modify: `internal/stream/nsq.go`. Generalise `Consume` so it also
  subscribes to topic `status` on its own ephemeral channel,
  `lierc-api-go-<id>#ephemeral`, with the same reconnect handling. Its
  handler calls `func (s *Server) statusChanged(ctx, body []byte)`, which
  decodes `{"Id","Connected"}`, finds the owner with `LookupOwner`
  (skipping an unknown connection), and sends `connection:connect` or
  `connection:disconnect` `{"Id"}` to the owner's streams with no SSE id.
- Modify: `internal/stream/replay.go` `replay`. Before the other events,
  emit one `sseEvent("connections", json, "")` built from `statuses`: `Id`,
  `Name` from `Config.Alias` or `Config.Host`, `Nick`, and
  `Connected` from `Status.Connected`. Remove the `CREATE` emit and the
  parameterless `CONNECT` emit.
- Modify: `cmd/lierc-api/main.go`. Start `go s.ListenConnections(ctx, d)`
  next to `Consume`.
- Test: `internal/stream/stream_test.go`. Update the replay tests at
  roughly lines 520–630, which expect `CREATE`, and add the new tests.

- [ ] **Step 1: Write the failing tests:**
  - **`TestConnectionNotifyCreateUpdateDelete`:** open a stream for u1 and
    `pg_notify` each action for c1. The client gets `connection:create`
    `{"Id":"c1","Name":"Net1","Nick":"me1"}`, then `connection:update`
    `{"Id":"c1","Name":"Net1"}`, then `connection:delete` `{"Id":"c1"}`.
    The delete is sent after the row is deleted.
  - **`TestConnectionNotifyNoStreams`:** a notification for a user without
    streams logs nothing.
  - **`TestConnectionNotifyRowGone`:** `create` for a missing row sends
    nothing.
  - **`TestConnectionNotifyMalformed`:** payload `not json`, then a valid
    delete. The valid one still arrives.
  - **`TestDeliverDropsCreateDelete`:** `Deliver` of a liercd `CREATE` and
    `DELETE` sends nothing.
  - **`TestDeliverPassesOldConnect`:** an old liercd's `CONNECT` and
    `DISCONNECT` still arrive as `irc` events.
  - **`TestStatusTopicEvents`:** publishing `{"Id":"c1","Connected":false}`
    and then `true` on `status` (testenv's nsqd) gives the owner's stream
    `connection:disconnect` and then `connection:connect` `{"Id":"c1"}`,
    with no SSE id. An unknown connection gives nothing.
  - **`TestReplaySnapshot`:** the first replay event is `connections`,
    listing every connection with `Name`, `Nick` and `Connected`. No
    `CREATE` or bare `CONNECT` follows.
  - **`TestListenReconnects`:** terminate the listener's backend with
    `pg_terminate_backend`. After `NSQReconnect`, a new notification is
    delivered.
- [ ] **Step 2: Run them and watch them fail.** Run
  `go test ./internal/stream/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them again.** Expected: PASS, with
  `go test -race ./...` and `go vet ./...` green.
- [ ] **Step 5: Commit** with "stream: connection events from Postgres
  NOTIFY and the status topic; snapshot on replay; drop CREATE/DELETE".

### Task 4: Perl API sends `pg_notify`

**Files:**
- Modify: `lib/API/DB.pm`. Add `sub notify_connection { my ($self, $user,
  $id, $action) = @_; ... }`. It runs `SELECT pg_notify('connections', ?)`
  with `encode_json { user => $user, connection => $id, action => $action
  }`.
- Modify: `lib/API/Controller/Connection.pm`:
  - `start` sends `create` once the row is saved, after the liercd request
    whatever it returned. When the limit refused the row, it sends nothing.
  - `delete` sends `delete` after `delete_connection`.
  - `edit` sends `update` on both return paths.
  - `save` sends `update`.
- Test: `t/connection_events.t`, using the FakeApp pattern from
  `t/connection_save.t`, with a `notify_connection` that records its
  arguments.

- [ ] **Step 1: Write the failing test.** Each controller action records
  exactly one `[user, id, action]`: create, update (both edit paths),
  update (save) and delete. A delete where liercd fails records none. A
  create refused by the limit records none.
- [ ] **Step 2: Run it and watch it fail.** Run `prove -Ilocal/lib/perl5
  -Ilib t/connection_events.t` in the API image, mounted as before.
  Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run it again.** Expected: PASS, with the whole `t/` suite
  green.
- [ ] **Step 5: Commit** with "API: notify the connections channel on
  create, edit, save and delete".

### Task 5: liercd emits `000` lines and `status` state; the logger stores `000` (lierc)

**Files:**
- Modify: `pkg/liercd/manager.go`:
  - Remove `CreateEvent`, `DeleteEvent` and their calls (`create` at
    about line 244, `destroy` at about line 259).
  - In `Status`, replace `ConnectEvent` with
    `func (m *ClientManager) StatusLine(s *lierc.IRCClientStatus, nick string) *lierc.IRCClientMessage`,
    which parses `:<Host> 000 <nick or *> :<Message>`. Enqueue it on
    `chats`, then enqueue `statusMessage{Id, Connected, Message, Time}` on
    `status`. Take the nick from the client, `currentNick()` via an
    exported accessor, or `*` if it's empty.
- Modify: `pkg/logger/classify.go` `logType`. `case "000":` returns
  `logStatus`.
- Test: `pkg/liercd`:
  - **`TestCreateDestroyPublishNoConnectionEvents`:** `create` and
    `destroy` through `HandleCommand`, with a recording queue, publish no
    `CREATE`, `DELETE`, `CONNECT` or `DISCONNECT`.
  - **`TestStatusPublishesLineAndState`:** a status of
    `{Connected:false, Message:"Reconnecting in 30s"}` enqueues
    `000 me :Reconnecting in 30s` on `chats` and
    `{"Id","Connected":false,...}` on `status`, in that order. An empty
    nick gives `*`.
- Test: `pkg/logger/classify_test.go`. Add the row `{"connection
  status", "000", ..., logStatus}`.

- [ ] **Step 1: Write the failing tests.**
- [ ] **Step 2: Run them and watch them fail.** Run
  `go test ./pkg/liercd/ ./pkg/logger/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them again.** Expected: PASS, with
  `go test -race ./...` green.
- [ ] **Step 5: Commit** with "liercd: status as a 000 line plus state on
  the status topic, no fake CREATE/DELETE/CONNECT/DISCONNECT; logger
  stores 000".

### Task 6: Dev-stack check, then deploy in order

- [ ] **Step 1: Dev stack.** Bring up `lierc-docker-compose` on port 8088,
  with the UI mounted from the branch and lierc-api and lierc built from
  the working trees (`docker-compose.build.yml`). Then, in headless
  Chromium:
  - add a network: one panel appears;
  - Save & Reconnect: the panels stay, show disconnected then connected,
    and are renamed. The status panel shows "Closing connection" and
    "Connected" as `000` lines exactly once each, and the sidebar's
    `disconnected` class toggles on and then off;
  - delete: the panels go;
  - a second tab sees all three;
  - delete while the second tab's stream is stopped, then resume: the
    snapshot removes the panels.

  `down -v` afterwards.
- [ ] **Step 2: UI** (with the go-ahead). Push branch `connection-events`,
  pin `lierc_minimal_ui_version` to its head, commit and push infra, and
  run the playbook with `--tags ui`. Check that the served `stream.js` has
  the new listeners.
- [ ] **Step 3: lierc-api** (with the go-ahead). Push `master` and wait for
  CI and both images. Bump `lierc_api`, `lierc_api_go` and `lctl`, commit
  and push infra, then dry-run and run the full playbook. Live checks:
  - delete a test connection: the panels go;
  - edit one: the panels stay;
  - the stream server logs `LISTEN connections` with no errors.
- [ ] **Step 4: lierc** (with the go-ahead). Push, wait for CI, bump
  `lierc` and `lierc_next`, then deploy. The logger, notifier and identd
  restart. Tell the user that liercd-next picks it up on its manual
  restart, and that the stream server drops its old `CREATE`/`DELETE`
  meanwhile.
- [ ] **Step 5: Merge the UI.** When the user says it looks good,
  fast-forward the UI's `master`, push, delete the branch, and repin the
  UI to `master`.
