# Test-only images for parity/compose.yml. Build context: the repo root.
#   target fakes:    the testenv fakes (liercd :5005, SMTP :2525, Imgur :8081,
#                    control API :8090)
#   target postgres: Postgres with lierc.sql loaded, after testenv's two
#                    in-memory syntax patches (lierc.sql itself is never edited)
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -o /fakes ./internal/testenv/cmd/fakes
COPY lierc.sql ./
RUN /fakes schema lierc.sql > /lierc.sql

FROM gcr.io/distroless/static AS fakes
COPY --from=build /fakes /fakes
EXPOSE 5005 2525 8081 8090
ENTRYPOINT ["/fakes"]

FROM postgres:16 AS postgres
COPY --from=build /lierc.sql /docker-entrypoint-initdb.d/lierc.sql
