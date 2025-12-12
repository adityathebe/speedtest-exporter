FROM gcr.io/distroless/static-debian12:nonroot

# Buildx sets these so we can pick the right binary that GoReleaser places in
# the Docker context (e.g., linux/amd64/speedtest-exporter).
ARG TARGETOS
ARG TARGETARCH

COPY ./${TARGETOS}/${TARGETARCH}/speedtest-exporter /speedtest-exporter

EXPOSE 7777
ENTRYPOINT ["/speedtest-exporter"]
