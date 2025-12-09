FROM golang:1.25.5-bookworm AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN make build

FROM alpine:3.20
RUN apk add --no-cache ca-certificates

COPY --from=builder /src/bin/speedtest-exporter /usr/local/bin/speedtest-exporter

EXPOSE 7777
ENTRYPOINT ["/usr/local/bin/speedtest-exporter"]
