FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/lierc ./cmd/lctl

FROM gcr.io/distroless/static
COPY --from=build /out/lierc /out/lctl /
EXPOSE 5003
# /lierc runs a service: `lierc api` (the default) or `lierc ingest`
# (docker run IMAGE ingest). The admin command is /lctl
# (docker run --entrypoint /lctl).
ENTRYPOINT ["/lierc"]
CMD ["api"]
