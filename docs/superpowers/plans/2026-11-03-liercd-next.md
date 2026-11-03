# A second liercd (`next`) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Individual connections can be moved onto a second liercd running lierc's `irc-go` branch, and every liercd call reaches the instance its connection is on.

**Architecture:** A nullable `connection.liercd` column names the instance (`NULL` = `main`, `'next'`). The two liercd clients — Perl `API::Liercd::request` and Go `liercd.Client.Do` — take the connection id from the start of each path, look up the column, and send the call to `LIERCD_HOST` or `LIERCD_NEXT_HOST`. `lctl move-connection` moves a connection; relaychat-infra runs `liercd-next` on `127.0.0.2:5005`; lierc's CI builds the `irc-go` branch.

**Tech Stack:** Perl (Role::Tiny, LWP, DBI, Test::More), Go 1.27 (pgx, net/http/httptest), Ansible, GitHub Actions.

**Spec:** `lierc-api/docs/superpowers/specs/2026-11-03-liercd-next-design.md`

## Global Constraints

- Instance names: `main` (stored as `NULL`) and `next`. No others.
- Hosts: `LIERCD_HOST` for `main` (default `127.0.0.1`, as today), `LIERCD_NEXT_HOST` for `next` (no default). Port 5005 on both.
- A `next` connection with `LIERCD_NEXT_HOST` unset is an error naming the instance (`liercd instance "next" has no host (LIERCD_NEXT_HOST)`); never a fallback.
- A path whose connection has no row routes to `main`. A path not starting with an id (none exist today) routes to `main`.
- No caching of the lookup.
- With no row set to `next`, every request goes exactly where it goes today.
- Branches: lierc-api `liercd-next` off `master`; lierc `master` (CI change only); relaychat-infra `main`. Commit per task; never push, merge or deploy without the user's go-ahead.
- Commit trailer: `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Perl tests run in the `lierc-api-test` image: `docker run --rm -v "$PWD":/src -w /src lierc-api-test perl -I/opt/lierc-api/local/lib/perl5 -Ilib t/<file>.t`. Go: `go test -race ./...` (testenv starts Postgres from `lierc.sql`).

## Review Focus

1. **`raw` (every message) for a `next` connection** reaches `next`. Pinned by Task 2's routing test on `raw`.
2. **Stream recreate-on-open for a `next` connection** recreates on `next`, not `main`. Pinned by Task 1 (`FindOrRecreate` via the routed client).
3. **A move whose create fails** leaves the column set and says so; a later recreate goes to the new instance. Pinned by Task 3.
4. **Moving a connection that's already on the target, unknown, or to `next` with no host** is refused before anything is destroyed. Pinned by Task 3.
5. **Path forms:** `id/status`, `/id/status`, `id/raw` with a body, an id containing no slash at all. Pinned by Tasks 1 and 2.

---

### Task 1: Go routing

**Files:**
- Modify: `lierc.sql` (column), `internal/config/config.go`, `internal/db/db.go`, `internal/liercd/liercd.go`, `cmd/lierc-api/main.go`, `internal/lctl/lctl.go`
- Test: `internal/liercd/liercd_test.go`, `internal/db/db_test.go` (or the package's existing test file)

**Interfaces:**
- Produces:
  - `lierc.sql`: `liercd text` on `connection` (nullable, no default).
  - `config.Config.LiercdNextHost string` from `LIERCD_NEXT_HOST` (default `""`).
  - `func (d *DB) LookupLiercd(ctx context.Context, id string) (string, error)` — `""` for NULL or no row.
  - `type Instances struct { Main, Next string }` (hosts) and `func NewRouted(hosts Instances, instanceOf func(ctx context.Context, id string) (string, error)) *Client` in package liercd. `Do` takes the id (path up to the first `/`, after trimming leading slashes), calls `instanceOf`, and builds the URL from `Main` for `""`, `Next` for `"next"`; `Next == ""` for `"next"` returns the Global Constraints error; any other name returns `unknown liercd instance %q`. `New(host)` stays (single host, no lookup) for tests and callers that want it.
  - `cmd/lierc-api/main.go` and `lctl.go` build their client with `NewRouted(liercd.Instances{cfg.LiercdHost, cfg.LiercdNextHost}, d.LookupLiercd)`.

- [ ] **Step 1: Write the failing tests.** `TestRoutedDo` with two `testenv.FakeLiercd` servers (their URLs' host:port replaces `host:5005`: give `Instances` full base URLs via an unexported `newRoutedBase(mainBase, nextBase, instanceOf)` that `NewRouted` wraps, so tests don't need port 5005). Table: instance `""` → main gets `GET c1/status`; `"next"` → next gets it; `/c1/status` same; `c1/raw` with body `PRIVMSG #a :hi` → body arrives on next; `"next"` with empty Next base → error containing `LIERCD_NEXT_HOST`, neither server called; `"bogus"` → error, neither called. `TestFindOrRecreateRoutes`: a `next` connection whose status is 404 is created on next. `TestLookupLiercd`: NULL → `""`, `'next'` → `"next"`, missing id → `""`.
- [ ] **Step 2: Run, expect failure** (`go test ./internal/liercd ./internal/db`): undefined `NewRouted`, `LookupLiercd`.
- [ ] **Step 3: Implement**, add the column to `lierc.sql`, switch the two constructors.
- [ ] **Step 4: Run** `go vet ./... && go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `Route liercd calls to the connection's instance (Go)`.

### Task 2: Perl routing

**Files:**
- Modify: `lib/API/Config.pm`, `lib/API/DB.pm`, `lib/API/Liercd.pm`
- Create: `t/liercd_route.t`

**Interfaces:**
- Produces:
  - `API::Config::next_host` → `$ENV{LIERCD_NEXT_HOST}` (undef when unset).
  - `API::DB::lookup_liercd($id)` → the column's value, or undef for NULL / no row.
  - `API::Liercd::liercd_url($path)`: id = `$path` up to the first `/` after leading slashes; instance = `lookup_liercd($id)`; host = `$self->host` for undef, `$self->next_host` for `next` (die with the Global Constraints message when it's undef or ""), die `unknown liercd instance "$x"` otherwise. `request` is unchanged apart from using it.

- [ ] **Step 1: Write the failing test** `t/liercd_route.t`: a small package composing `API::Liercd` (`Role::Tiny::With`) with stub `host` (`"main.example"`), `next_host`, `lookup_liercd` (from a hash), and a `ua` whose `request` records the URL and returns a 200 `HTTP::Response`. Assert: `c1/status` → `http://main.example:5005/c1/status`; a `next` id → `http://next.example:5005/...`; `/c2/raw` → next; unknown id → main; `next` with `next_host` undef → dies matching `LIERCD_NEXT_HOST`, no request made; `bogus` → dies; `find_or_recreate_connection` for a `next` id sends status/create/status to next.
- [ ] **Step 2: Run, expect failure** (in the `lierc-api-test` image).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run every `t/*.t`** in the image → all pass.
- [ ] **Step 5: Commit** `Route liercd calls to the connection's instance (Perl)`.

### Task 3: `lctl move-connection`

**Files:**
- Create: `internal/lctl/move_connection.go`, `internal/lctl/move_connection_test.go`
- Modify: `internal/lctl/commands.go`, `internal/db/db.go`

**Interfaces:**
- Consumes: Task 1's `LookupLiercd`, routed `env.Liercd`, existing `destroy(ctx, env, id) string` and `create(ctx, env, id, config) error` in `reconnect.go`.
- Produces: `func (d *DB) SetLiercd(ctx context.Context, id, instance string) error` (`""` stores NULL); command `move-connection ID main|next`, Summary `Move a connection to another liercd instance (main or next)`.

Behaviour, in order:
1. Exactly two args; target must be `main` or `next` (else `UsageError`).
2. Unknown connection → `unknown connection ID`. Already there → `ID is already on TARGET`. Target `next` with `LIERCD_NEXT_HOST` unset → error naming it. Nothing is destroyed for any of these.
3. Destroy on the current instance (404 fine); on any other failure, stop with the error and leave the column.
4. `SetLiercd`.
5. Create on the target from the saved config. On failure print `ID is now on TARGET, but creating it there failed: ERR; it will be created when its user's stream next opens, or run lctl reconnect ID`, and return a non-nil error.
6. Success prints `moved ID from SOURCE to TARGET`.

- [ ] **Step 1: Write the failing tests** with two fake liercds and the test Postgres (follow `connections_test.go`/`helpers_test.go` for setting up a user and connection): a move main→next destroys on main, sets the column, creates on next, in that order; next→main the reverse; each refusal in step 2 calls neither liercd and leaves the column; destroy 500 stops before the column changes; create 500 leaves the column set, prints the message, returns an error.
- [ ] **Step 2: Run, expect failure.**
- [ ] **Step 3: Implement**, register in `commands.go` after `reconnect`.
- [ ] **Step 4: Run** `go vet ./... && go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `Add lctl move-connection`.

### Task 4: lierc CI builds the irc-go branch

**Files:** Modify: `lierc/.github/workflows/deploy.yml` (in the lierc repo, on `master`).

- [ ] **Step 1:** `on.push.branches` gains `irc-go`. The metadata tags become `type=raw,value=latest,enable={{is_default_branch}}` and `type=sha,format=short`, so `latest` only moves on `master`.
- [ ] **Step 2:** Validate with `actionlint` if installed, else `python3 -c 'import yaml,sys; yaml.safe_load(open(sys.argv[1]))'` on the file.
- [ ] **Step 3: Commit** `Build images for the irc-go branch too, without moving latest`.

### Task 5: relaychat-infra

**Files:** Modify: `roles/services/defaults/main.yaml`, `roles/services/templates/lierc.env.j2`, `group_vars/relaychat_hosts/vars.yaml`. Create: `roles/services/templates/liercd-next.service.j2`.

- [ ] **Step 1:** `liercd-next.service.j2` is `liercd.service.j2` with `Description=Liercd (next)`, `{{ images.lierc_next }}` (both places) and `-e LISTEN=127.0.0.2:5005`.
- [ ] **Step 2:** `services_units` gains `- name: liercd-next` with `manual_restart: true` and the same comment as liercd, after `liercd`.
- [ ] **Step 3:** `lierc.env.j2` gains `LIERCD_NEXT_HOST=127.0.0.2` after `LIERCD_HOST`.
- [ ] **Step 4:** `vars.yaml` `images` gains `lierc_next: ghcr.io/lierc/lierc:sha-<irc-go head once built> # liercd-next, the irc-go branch` — left as a placeholder line commented out until the image exists (the deploy step fills it in).
- [ ] **Step 5:** Check how the services role iterates `services_units` (templates by name, handlers) so the new unit is templated, enabled and started like the others; adjust only if needed.
- [ ] **Step 6: Dry run** `ansible-playbook --vault-password-file ~/.config/relaychat-vault-pass site.yaml --check --diff` (only after Step 4 has a real image, or with Step 4 uncommented against a known-good image tag for the dry run) and confirm the diff is: the new unit, the env line, nothing else.
- [ ] **Step 7: Commit** `Add liercd-next, a second liercd for trying the irc-go branch`.

### Task 6: End-to-end in the dev stack

Throwaway, nothing committed.

- [ ] **Step 1:** Compose override (scratchpad) adding `liercd-next` (the `irc-go` lierc image built locally, `LISTEN=0.0.0.0:5005`, its own container name) and `LIERCD_NEXT_HOST=liercd-next` for lierc-api and lierc-api-stream; lierc-api built from the `liercd-next` branch.
- [ ] **Step 2:** Sign up, `ALTER` is already in `lierc.sql` for a fresh volume. Run `lctl move-connection <id> next` in a one-off api-go container on the stack's network. Confirm in `liercd-next`'s log that it registered, send a message from the UI (headless Chromium, as before) and see it in `liercd-next`'s log, reload the app (stream recreate) and see no create on `main`; move back and see the reverse.
- [ ] **Step 3:** `down -v`, remove built images.

### Task 7: Rollout (only on the user's go-ahead)

1. `ALTER TABLE connection ADD COLUMN liercd text` on production.
2. Merge lierc-api `liercd-next` to `master`, push, wait for both images, bump `lierc_api` and `lierc_api_go`, deploy.
3. Push lierc `master` (CI) and `irc-go`; wait for `sha-<irc-go head>`.
4. Fill in `images.lierc_next`, deploy relaychat-infra, start `liercd-next` (it's `manual_restart`, so `systemctl start` it by hand).
5. Move one of the user's own connections with `sudo lctl move-connection ID next`.
