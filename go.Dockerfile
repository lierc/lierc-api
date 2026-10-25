FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /lierc-api ./cmd/lierc-api

FROM gcr.io/distroless/static
COPY --from=build /lierc-api /lierc-api
EXPOSE 5003
ENTRYPOINT ["/lierc-api"]
CMD ["stream"]
