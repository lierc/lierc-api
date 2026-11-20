module github.com/lierc/lierc-api

go 1.27

require (
	github.com/Masterminds/squirrel v1.5.4
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/VictoriaMetrics/metrics v1.44.1
	github.com/jackc/pgx/v5 v5.7.6
	github.com/lierc/lierc v0.0.0-20261120211019-bea31d36024c
	github.com/nsqio/go-nsq v1.1.0
	github.com/olekukonko/tablewriter v1.1.5
	github.com/wneessen/go-mail v0.8.2
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/term v0.47.0
	golang.org/x/text v0.43.0
	golang.org/x/time v0.16.0
)

require (
	github.com/BurntSushi/toml v1.3.2 // indirect
	github.com/blang/semver v3.5.1+incompatible // indirect
	github.com/bmizerany/perks v0.0.0-20141205001514-d9a9656a3a4b // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/displaywidth v0.10.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.6.0 // indirect
	github.com/ergochat/irc-go v0.7.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/judwhite/go-svc v1.2.1 // indirect
	github.com/julienschmidt/httprouter v1.3.0 // indirect
	github.com/lann/builder v0.0.0-20180802200727-47ae307949d0 // indirect
	github.com/lann/ps v0.0.0-20150810152359-62de8c46ede0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/mreiferson/go-options v1.0.0 // indirect
	github.com/nsqio/go-diskqueue v1.1.0 // indirect
	github.com/nsqio/nsq v1.3.0 // indirect
	github.com/olekukonko/cat v0.0.0-20250911104152-50322a0618f6 // indirect
	github.com/olekukonko/errors v1.2.0 // indirect
	github.com/olekukonko/ll v0.1.6 // indirect
	github.com/stretchr/testify v1.10.0 // indirect
	github.com/valyala/fastrand v1.1.0 // indirect
	github.com/valyala/histogram v1.2.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

tool github.com/nsqio/nsq/apps/nsqd

replace github.com/judwhite/go-svc => github.com/mreiferson/go-svc v1.2.2-0.20210815184239-7a96e00010f6
