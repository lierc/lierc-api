# Perl/Go stream parity suite

Opens the same streams against the Perl stream server (`bin/stream.psgi`) and
the Go one (`lierc-api stream`) and diffs what they send.

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
| `fakes`       | `internal/testenv/cmd/fakes`: liercd :5005, control API :8090  | 18090 (control) |
| `perl-api`    | the repo's `Dockerfile`, default CMD (Gazelle); only registers the test user | 5004 |
| `perl-stream` | same image, the production Twiggy command                      | 5003      |
| `go-stream`   | `go.Dockerfile`, `lierc-api stream`                            | 5013      |

All servers share one environment: the same `API_SECRET`, `LIERCD_HOST=fakes`,
Postgres and nsqd. `LIERC_NO_SMTP` is set so Perl's `/register` sends no mail.

`lierc.sql` has two statements Postgres rejects. It is never edited: the
`postgres` image is built (`fakes.Dockerfile`, target `postgres`) by running
`fakes schema lierc.sql`, which applies the same in-memory patches as
`internal/testenv` (`testenv.PatchSchema`).

The test endpoints can be moved with `PARITY_PERL_API`, `PARITY_PERL_STREAM`,
`PARITY_GO_STREAM`, `PARITY_DB`, `PARITY_NSQD`, `PARITY_FAKES` and
`PARITY_API_KEY`.

## What is compared

The test user registers on the Perl API, and the `chats` cookie Perl issues
logs in to both stream servers.

- `TestStreamParity` compares `GET /` and `/stats` without credentials, then
  opens one stream per server for the same user and compares the
  `Content-Type`, padding, first ping, replay (per `push_fake_events` step,
  order within a step ignored), `/stats` with the key, and delivery of 20 NSQ
  messages in order.
- `TestStreamReplayOneConnectionFails` does the same with one connection whose
  liercd create fails.
- `compare.go` diffs status, `Content-Type` and body. JSON is compared by
  meaning: key order ignored, value types strict (a number is not a string,
  `null` is not `""`), numbers by value. Anything that may differ is allowed by
  path with a reason (`Allow`): stream start times, client addresses, and the
  ping time (`sameFormat`: the text format must match, digits may differ).

`compare_test.go` unit-tests the comparison; it runs without the stack, in the
normal `go test ./...`.

## CI

The `parity` job in `.github/workflows/go.yml` builds and starts the stack on
`ubuntu-latest`, runs the suite, prints `docker compose logs` on failure and
always tears down. It does not gate the `image` job yet; make the image job
need it once the suite has been green.

## First CI run: confirm these

The Go code had to guess Perl's behaviour here from reading the source. The
parity comparison is the source of truth: if one fails, fix Go and update the
Go test named, which currently hard-codes the guess.

| Item | Parity test | Go test with the guess |
|------|-------------|------------------------|
| Stream replay when one connection's liercd create fails (Go sends no replay events and keeps the stream open) | `TestStreamReplayOneConnectionFails` | `internal/stream` `TestReplayOneConnectionFails` |
| `/stats` output, with and without `?user=` | `TestStreamParity` | `internal/stream` `TestStatsKey` |

After the first green run, pin the Perl-recorded behaviour in
`TestReplayOneConnectionFails`.
