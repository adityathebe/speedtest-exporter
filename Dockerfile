FROM golang:1.25.5-bookworm AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN make build

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /src/bin/speedtest-exporter /speedtest-exporter

EXPOSE 7777
ENTRYPOINT ["/speedtest-exporter"]
