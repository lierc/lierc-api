# Perl/Go parity suite

Runs the same scenarios against the Perl servers (`bin/api.psgi`,
`bin/stream.psgi`) and the Go servers (`lierc-api api|stream`) and diffs the
results.

```sh
docker compose -f parity/compose.yml up -d --build --wait
go test -tags parity -timeout 45m -v ./parity/
docker compose -f parity/compose.yml down -v
```

The first build of the Perl image compiles Perl and every CPAN dependency, so
it takes a long time.

## The stack (`compose.yml`)

| service       | what                                                           | host port |
|---------------|----------------------------------------------------------------|-----------|
| `postgres`    | Postgres 16 with `lierc.sql` loaded                            | 15432     |
| `nsqd`        | nsqd 1.3.0 (HTTP for publishing)                               | 14151     |
| `fakes`       | `internal/testenv/cmd/fakes`: liercd :5005, SMTP :2525, Imgur :8081, control API :8090 | 18090 (control) |
| `perl-api`    | the repo's `Dockerfile`, default CMD (Gazelle)                 | 5004      |
| `perl-stream` | same image, the production Twiggy command                      | 5003      |
| `go-api`      | `go.Dockerfile`, `lierc-api api`                               | 5014      |
| `go-stream`   | `go.Dockerfile`, `lierc-api stream`                            | 5013      |

All four servers share one environment: the same `API_SECRET`,
`LIERCD_HOST=fakes`, Postgres, nsqd and SMTP settings, and
`LIERC_IMGUR_URL=http://fakes:8081` (only Go reads it; Perl's Imgur URL is
hard-coded, so only the image paths that never reach Imgur are compared).
`VAPID_PUBLIC` is unset on purpose.

`lierc.sql` has two statements Postgres rejects. It is never edited: the
`postgres` image is built (`fakes.Dockerfile`, target `postgres`) by running
`fakes schema lierc.sql`, which applies the same in-memory patches as
`internal/testenv` (`testenv.PatchSchema`).

The test endpoints can be moved with `PARITY_PERL_API`, `PARITY_PERL_STREAM`,
`PARITY_GO_API`, `PARITY_GO_STREAM`, `PARITY_DB`, `PARITY_NSQD`,
`PARITY_FAKES` and `PARITY_API_KEY`.

## What is compared

- `TestScenarios` runs each scenario in `scenarios.go` twice: truncate every
  table (`RESTART IDENTITY`, so seeded log ids match) and reset the fakes, run
  it against Perl, then truncate, reset and run it against Go. Each server run
  has its own cookie jars, one per user. Every route in `API::Routes` has a
  success case and an error case.
- `compare.go` diffs status, `Content-Type` and body. JSON is compared by
  meaning: key order ignored, value types strict (a number is not a string,
  `null` is not `""`), numbers by value. Event-stream bodies are compared
  event by event. Cookies are never compared (Perl writes `chats`, Go
  `chats2`).
- Ids and tokens captured in a run (`Capture`, `CaptureSQL`) are replaced by
  `{{name}}` in that run's results before comparing. Anything else that may
  differ is listed next to its step with a reason (`Allow`): generated tokens,
  timestamps (`sameFormat`: the text format must match, digits may differ),
  error text from library or crash paths (`libraryError`: the JSON type must
  still match, so `null` against a string fails), and the owner-check message
  (`pattern`).
- `TestMigrationFromPerl`, `TestPerlLogoutCookie`, `TestGoLogoutStaysLoggedOut`
  replay Perl's `chats` cookie against Go.
- `TestStreamParity` opens one stream per server for the same user and
  compares the padding, first ping, replay (per `push_fake_events` step, order
  within a step ignored), `/stats`, and delivery of 20 NSQ messages in order.
  `TestStreamReplayOneConnectionFails` does the same with one connection whose
  liercd create fails.

`compare_test.go` unit-tests the comparison and checks that every scenario
step is well formed; it runs without the stack, in the normal `go test ./...`.

## CI

The `parity` job in `.github/workflows/go.yml` builds and starts the stack on
`ubuntu-latest`, runs the suite, prints `docker compose logs` on failure and
always tears down. It does not gate the `image` job yet; make the image job
need it once the suite has been green.

## First CI run: confirm these

These are places where the Go code (and its unit tests) had to guess Perl's
output from reading the source. The parity comparison is the source of truth:
if a step below fails, fix Go and update the Go test named, which currently
hard-codes the guess.

| Item | Parity step | Go test with the guess |
|------|-------------|------------------------|
| `/privates` `time` format (Postgres text, no `T`, no zone) | `privates` / `GET /privates` | `internal/api` `TestPrivates` |
| Image `created` format (`created::text`) | `images` / `GET /image` | `internal/api` `TestImageList`, `TestImageUpload` (only non-empty) |
| `Config` in `/connection` (stored text passed through as `json.RawMessage`; Perl decodes and re-encodes) | `connection` / `GET /connection`, `GET /connection/{{conn}}` | `internal/api` `TestListAndShow` |
| `/verify` with an unknown token (Go answers ok: `selectcol_arrayref` returns a true `[]`) | `verify` / `verify unknown token` | `internal/api` `TestVerify` |
| Stream replay when one connection's liercd create fails (Go sends no replay events and keeps the stream open) | `TestStreamReplayOneConnectionFails` | `internal/stream` `TestReplayOneConnectionFails` |
| `Invalid connection id 'API::Request=HASH(0x…)->captures->{id}'` | `ownership` (compared by pattern) | `internal/api` `TestOwnerCheck` |
| Method mismatch on an owned path gives 404 (and on someone else's path) | `ownership` / `PATCH owned connection`, `PATCH someone else's connection` | `internal/api` `TestMethodMismatch` |
| JSON types of `MessageId`, `Highlight`, `Self`; `/missed` counts; `/seen` `message_id` | `logs` | `internal/api` `TestLog250`, `TestHighlight`, `TestMissed`, `TestSeen` |
| `null` for an unset `VAPID_PUBLIC` | `web push` / `GET /notification/web_push_keys` | `internal/api` `TestWebPushKeys` |

Also new in this suite, with no Go unit test yet: `GET /connection/0` (Perl
skips the owner check for a false id), `GET /image?limit=abc` and
`?page=abc` (Perl numifies them), `PUT /connection/:id` when liercd's destroy
fails (Perl's edit ignores both errors and answers ok), the registration
email's `Content-Type` and charset, and the stream's `/stats`.

After the first green run, pin the Perl-recorded values in `TestVerify` and
`TestReplayOneConnectionFails`, and the JSON value types of timestamps in the
Task 7–9 tests.
