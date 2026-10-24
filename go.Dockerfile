FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lierc-api ./cmd/lierc-api

FROM gcr.io/distroless/static
COPY --from=build /lierc-api /lierc-api
# .apn/ holds secrets and is mounted at /opt/lierc-api/.apn in production.
WORKDIR /opt/lierc-api
COPY icon.iconset/ icon.iconset/
EXPOSE 5004 5003
ENTRYPOINT ["/lierc-api"]
CMD ["api"]
