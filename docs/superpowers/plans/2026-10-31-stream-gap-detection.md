# Stream Gap Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The page notices and fetches messages it missed while its event stream looked connected, and detects a dead stream in one round trip after waking.

**Architecture:** The Go stream server tracks each online user's newest message ID in memory and puts it in every ping. Each stream announces its ID so the page can ping that stream on wake. The page compares the ping's ID with the newest it has seen and fetches the gap from `/log/<id>`.

**Tech Stack:** Go (`internal/stream`, `internal/db`, pgx; tests use `internal/testenv` Postgres and nsqd), plain ES modules in lierc-minimal-ui (no build step, no test framework).

**Spec:** `docs/superpowers/specs/2026-10-31-stream-gap-detection-design.md`

## Global Constraints

- Ping event data is JSON: `{"time":<hires time>,"last_id":<int>}`. `time` is `hiresTime()` emitted as a JSON number; `last_id` is `0` when the user has no logged messages.
- Stream ID event: `event: stream` with data `{"id":"<stream id>"}`, written after the padding and before the first ping.
- Wake ping: `POST` to the stream server path `/<stream id>/ping`, with any number of leading slashes (prod nginx sends `//<id>/ping` until Task 2b is deployed; dev sends `/<id>/ping`). Responses: `204` and a ping queued on that stream; `404` `unknown stream` (JSON error body via `writeError`) when the session's user has no stream with that ID; `401` `unauthorized` without a valid session.
- Wake ping timeout: 5 seconds without any stream event after the ping → reconnect.
- `/auth` every 15 s, the 15 s heartbeat timeout, and the `online`/`offline` handlers are unchanged.
- Deploy order: the stream server (`images.lierc_api_go`) before the UI. Current clients ignore ping data and unknown event types.
- relaychat-infra and lierc-api commits go to `main`, lierc-minimal-ui commits to `master`, locally. Pushing, `make bust`, image bumps and deploys happen only when the user asks.

## Review Focus

- A live message and the same message from a gap fetch both arrive: it must appear once, and unread counts must not double. Task 4 dedupes by message ID in `Stream`.
- Messages arrive slightly out of ID order (several logger workers): none may be dropped. Dedupe is by a set of seen IDs, never by "ID <= last_id". Task 4 tests it.
- A user with two tabs: a wake ping from one tab must only ping that tab's stream, and the other tab's ping data must still carry the shared newest ID. Task 3 tests it.
- The stream server restarts while a tab is asleep: the wake ping gets 404 and the page reconnects at once. Task 3 tests the 404; Task 4 tests the reconnect.
- `/log/<id>` returns "Too many messages!" after a long sleep: `fill_missed` keeps calling `reset()` as today, and the in-flight guard is cleared. Task 5 tests it.

---

### Task 1: Newest message ID query

**Files:**
- Modify: `internal/db/db.go` (after `LookupOwner`)
- Test: `internal/db/db_test.go`

**Interfaces:**
- Produces: `func (d *DB) LatestMessageID(ctx context.Context, user string) (int64, error)`: `SELECT COALESCE(max(id), 0) FROM log WHERE "user"=$1`.

- [ ] **Step 1: Write the failing test** `TestLatestMessageID`: a new user gives `0, nil`. Insert three `log` rows (two for alice with ids 5 and 9, one for bob with id 12; `channel`, `time`, `message` filled with `'#a'`, `now()`, `'{}'`) and expect alice → `9`, bob → `12`, unknown user → `0`.
- [ ] **Step 2: Run** `go test ./internal/db -run TestLatestMessageID`. Expected: FAIL, `LatestMessageID` undefined.
- [ ] **Step 3: Implement** `LatestMessageID` as above.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `git add internal/db && git commit -m "Add DB.LatestMessageID"`

### Task 2: Pings carry the newest message ID; streams announce their ID

**Files:**
- Modify: `internal/stream/server.go` (Server fields, `pingAll`, `deregisterLocked`, `Deliver`, `pingEvent`)
- Modify: `internal/stream/writer.go` (`serveStream` handshake)
- Modify: `docs/go-stream.md` (handshake and ping bullets)
- Test: `internal/stream/stream_test.go`

**Interfaces:**
- Consumes: `DB.LatestMessageID` (Task 1).
- Produces:
  - `Server.latest map[string]int64` (user → newest ID), guarded by `s.mu`, created in `New`.
  - `func (s *Server) raiseLatestLocked(user string, id int64)`: sets the max. `s.mu` must be held.
  - `func pingEvent(lastID int64) []byte`: the JSON ping from Global Constraints.
  - `func (s *Server) pingLocked(st *stream)`: `s.offerLocked(st, pingEvent(s.latest[st.user]))`. Task 3 uses it.
  - `func streamEvent(id string) []byte`: the `stream` event.

- [ ] **Step 1: Write the failing tests**
  - Update `TestHandshake`: after the padding, expect exactly `event: stream\ndata: {"id":"<id>"}\n\n` (match `^event: stream\ndata: \{"id":"[^"]+"\}\n\n$`), then a ping matching `^event: ping\ndata: \{"time":\d{10}(\.\d+)?,"last_id":0\}\n\n$`.
  - `TestPingLastID`: alice has `log` rows up to id 9 and bob up to id 50. Alice's handshake ping has `last_id` 9. Publish `{"ConnectionId":"ca","MessageId":20}` on `logged` and read it with `nextIRC`; then `e.s.pingAll()` and the next ping for alice has `last_id` 20. Publish `{"ConnectionId":"ca","MessageId":15}`; after `pingAll`, `last_id` is still 20. Bob's stream pings carry 50 throughout.
  - `TestLatestDroppedWithLastStream`: open two streams for alice, close one, and poll that `e.s.latest[alice]` is still set; close the other and poll that the entry is gone (read it under `e.s.mu`).
  - The `client`'s parser and helpers (`next`, `nextIRC`, `replayEvents`) treat `stream` events like pings: skipped by `nextIRC`, and the first event `replayEvents` checks for is `stream` then `ping`.
- [ ] **Step 2: Run** `go test -race ./internal/stream`. Expected: the three tests FAIL.
- [ ] **Step 3: Implement**
  - In `serveStream`, after `s.register(st)` and before the handshake write: `LatestMessageID` for the user, then `raiseLatestLocked` under `s.mu`. Registering first means a concurrent `Deliver` raises the value too, so the max is right either way. On a query error, log it and carry on with what is there.
  - The handshake write becomes `padding + streamEvent(st.id) + pingEvent(latest)`.
  - `Deliver`: after the owner lookup, under `s.mu`, `raiseLatestLocked(owner, n)` when `MessageId` parses as an int64 (`strconv.ParseInt(id, 10, 64)`); non-numeric IDs are ignored.
  - `pingAll` calls `pingLocked` for each stream.
  - `deregisterLocked` deletes `s.latest[user]` when it deletes `s.streams[user]`.
  - Marshal the ping with a struct `{Time json.Number "time"; LastID int64 "last_id"}`.
- [ ] **Step 4: Run** `go test -race ./internal/stream`. Expected: PASS, including every existing test.
- [ ] **Step 5: Update** `docs/go-stream.md`: the handshake is padding, a `stream` event with the stream's ID, then a ping; every ping's data is `{"time","last_id"}`, with `last_id` the newest logged message ID for the user.
- [ ] **Step 6: Commit** `git add internal/stream docs/go-stream.md && git commit -m "Send the newest message ID in stream pings"`

### Task 2b: Prod nginx forwards stream paths without a double slash

**Files (relaychat-infra):**
- Modify: `roles/nginx/templates/relaychat.conf.j2` (`location /api/events`)

**Interfaces:**
- Produces: the stream server receives `/api/events` as `/` and `/api/events/<rest>` as `/<rest>`, as the dev stack's `lierc-docker-compose/lierc-nginx/default.conf` already does. Task 3 still accepts extra leading slashes, so the deploy order doesn't matter.

- [ ] **Step 1: Change** the location to `rewrite ^/api/events/?(.*)$ /$1 break;` followed by `proxy_pass http://127.0.0.1:5003;` (no URI on `proxy_pass`). Keep the location's other directives.
- [ ] **Step 2: Dry run** `ansible-playbook --vault-password-file ~/.config/relaychat-vault-pass site.yaml --check --diff`. Expected: the nginx diff shows only those two lines, and `Check nginx config` passes.
- [ ] **Step 3: Commit** `git add roles/nginx/templates/relaychat.conf.j2 && git commit -m "Forward /api/events paths to the stream server without a double slash"`. Running the playbook is the user's call. Afterwards, check that `https://relaychat.party/app/` still streams (pings every 5 s in the browser's EventStream view).

### Task 3: Wake ping route

**Files:**
- Modify: `internal/stream/server.go` (`ServeHTTP`, new `servePing`)
- Modify: `docs/go-stream.md` (route list)
- Test: `internal/stream/stream_test.go`

**Interfaces:**
- Consumes: `pingLocked`, `streamEvent` (Task 2).
- Produces: `POST /<stream id>/ping` per Global Constraints. Task 4 calls it as `${baseurl}/events/${stream_id}/ping`.

- [ ] **Step 1: Write the failing tests** (add an `e.post(path, user string) *http.Response` helper next to `e.get`; get the stream's ID from its `stream` event):
  - `TestWakePing`: alice opens two streams. `POST /<first id>/ping` gives 204, and the first stream's next event is a ping with alice's `last_id`; after a further `pingAll`, the second stream has received exactly one ping since its handshake (only the `pingAll` one).
  - `TestWakePingDoubleSlash`: `POST //<id>/ping` gives 204.
  - `TestWakePingUnknown`: `POST /nope/ping` as alice gives 404 with body `{"status":"error","error":"unknown stream"}`. `POST /<alice's id>/ping` as bob gives 404. After alice's stream closes (poll `e.s.count(alice) == 0`), `POST /<its id>/ping` gives 404.
  - `TestWakePingUnauthorized`: no cookie gives 401.
  - `TestStreamStillServedOnOtherPaths`: `GET /x/ping` with a cookie opens a stream (200, `text/event-stream`), so only POST is routed to the ping.
- [ ] **Step 2: Run** `go test -race ./internal/stream -run 'WakePing|OtherPaths'`. Expected: FAIL.
- [ ] **Step 3: Implement** `func (s *Server) servePing(w http.ResponseWriter, r *http.Request, id string)`. `ServeHTTP` sends `POST` requests whose path matches `^/+([^/]+)/ping$` there, after the `/stats` check. It does the same session check and `UserExists` lookup as the stream (401 otherwise), then, under `s.mu`, finds `s.streams[user][id]` and either calls `pingLocked` and writes 204, or `writeError(w, 404, "unknown stream")`.
- [ ] **Step 4: Run** `go test -race ./internal/stream`. Expected: PASS.
- [ ] **Step 5: Update** `docs/go-stream.md` with the `POST /<stream id>/ping` bullet.
- [ ] **Step 6: Commit** `git add internal/stream docs/go-stream.md && git commit -m "Add a ping route for a client's own stream"`

### Task 4: Stream client: stream ID, gap check, dedupe, wake ping

**Files (lierc-minimal-ui):**
- Modify: `static/js/lierc/stream.js`
- Test: a throwaway Node harness in the session scratchpad (never committed), copying `stream.js` to `.mjs` and stubbing `EventSource`, `fetch`, `setTimeout`/`setInterval` and `Date.now`.

**Interfaces:**
- Consumes: the `stream` event, ping JSON and the ping route (Tasks 2 and 3).
- Produces (used by Task 5):
  - `stream.last_id`: a number or `null`, only ever raised.
  - `stream.latest_id`: the newest ID from the last ping, or `null`.
  - `stream.saw(id) -> bool`: records a message ID, raises `last_id`, and returns `false` if the ID was already seen. It keeps the last 1000 IDs (a `Set` plus FIFO order).
  - The `"behind"` event, fired with `last_id` when a ping's `last_id` is greater than `stream.last_id`.

- [ ] **Step 1: Write the harness cases** (each asserts on fired events, fetch calls and `EventSource` instances):
  1. First ping `{"last_id":40}` with `last_id` `null` sets `last_id` 40 and fires no `behind`.
  2. With `last_id` 40, ping 45 fires `behind` with 40; ping 40 fires nothing.
  3. Live `irc` events with `MessageId` 50 then 49 both fire `message`; `last_id` is 50. A repeated 50 fires nothing.
  4. A `stream` event sets `stream_id`; `#connect` clears it to `null`.
  5. `wake()` while open with `stream_id` "s1" POSTs `/api/events/s1/ping` (`credentials: 'same-origin'`). On 404, and on a fetch rejection, the `EventSource` is closed and a new one created without backoff. On 204 followed by a ping within 5 s, nothing more happens; on 204 with no event for 5 s, it reconnects.
  6. `wake()` with no open stream or no `stream_id` falls back to `check(wake_timeout)`.
  7. A ping whose data isn't JSON (an old server) still counts as a heartbeat and fires no `behind`.
- [ ] **Step 2: Run** `node <harness>.mjs`. Expected: cases 1-6 FAIL.
- [ ] **Step 3: Implement** in `stream.js`:
  - A `stream` listener and an `onping` that calls `touch()`, parses `last_id`, sets `latest_id`, sets or compares against `last_id`, and fires `behind`.
  - `onmessage` calls `saw(data.MessageId)` for events with an ID, and skips the `message` fire when it returns `false`.
  - `wake()` does the ping above, keeping `wake_timeout` only for the fallback.
  - `destroy()` removes the new listeners.
- [ ] **Step 4: Run** the harness. Expected: all cases PASS. Also `node --check` the `.mjs` copy.
- [ ] **Step 5: Commit** `git add static/js/lierc/stream.js && git commit -m "Detect missed messages and ping the stream on wake"`

### Task 5: App: fetch gaps, fix reconnect catch-up

**Files (lierc-minimal-ui):**
- Modify: `static/js/lierc/app.js` (`connect()`'s `open` handler and a new `behind` handler; `fill_missed`)
- Test: the Task 4 harness, extended with a stub `App`-like object holding `fill_missed` (or the method extracted and bound to a stub), and `api.get` stubbed.

**Interfaces:**
- Consumes: `stream.last_id`, `stream.latest_id`, `stream.saw`, `behind` (Task 4).

- [ ] **Step 1: Write the harness cases**
  1. `open` with `last_id` 40 and no focused panel calls `/log/40`.
  2. `behind` with 40 calls `/log/40` and fires `message` for each event whose `saw()` is true; afterwards `last_id` is the highest fetched ID.
  3. A second `behind` while a fill is in flight starts no second request. When the fill ends, if `latest_id > last_id`, exactly one more fill starts from the new `last_id`.
  4. `/log` rejects (as with "Too many messages!"): `reset()` is called and a later `behind` can fill again.
- [ ] **Step 2: Run** the harness. Expected: cases FAIL.
- [ ] **Step 3: Implement**
  - The `open` handler calls `this.fill_missed(stream.last_id)` whenever `stream.last_id` is set, focused panel or not.
  - `stream.on('behind', (id) => this.fill_missed(id))`.
  - `fill_missed` gets an in-flight flag (`this.filling_missed`), cleared in `finally`. It routes each event through `this.stream.saw(e.MessageId)` before firing `message`, and re-runs once from `stream.last_id` when `stream.latest_id` is ahead at the end.
- [ ] **Step 4: Run** the harness. Expected: PASS. `node --check` the `.mjs` copies of `app.js` and `stream.js`.
- [ ] **Step 5: Manual check** in the docker-compose dev stack (`lierc-api-stream` built from this branch with `docker-compose.build.yml`, the UI mounted from the working tree):
  - Pings show `last_id` in the browser's EventStream view.
  - Hiding and showing the tab sends one `POST /api/events/<id>/ping` → 204.
  - After `docker compose restart lierc-api-stream` while the tab is hidden, showing it gets a 404 and an immediate reconnect.
  - A message sent from another client while this tab's stream is blocked (devtools offline for 20 s, then back) appears without a reload.
- [ ] **Step 6: Commit** `git add static/js/lierc/app.js && git commit -m "Fetch missed messages on gaps and after every reconnect"`
