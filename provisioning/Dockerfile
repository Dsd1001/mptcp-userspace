# Build from repository root: docker build -f provisioning/Dockerfile .
FROM golang:1.25 AS build
WORKDIR /src
COPY macos/engine/go.mod ./macos/engine/go.mod
COPY macos/engine/tunnelconfig ./macos/engine/tunnelconfig
COPY provisioning/go.mod ./provisioning/go.mod
COPY provisioning/*.go ./provisioning/
COPY provisioning/web ./provisioning/web
COPY provisioning/VERSION ./provisioning/VERSION
ARG SOURCE_ID=unfrozen-container-build
WORKDIR /src/provisioning
RUN mkdir -p /out/data && chown 65532:65532 /out/data && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=$(cat VERSION) -X main.SourceID=${SOURCE_ID}" -o /out/mpx-provision .
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/data /data
WORKDIR /data
COPY --from=build /out/mpx-provision /usr/local/bin/mpx-provision
ENV MPX_PROVISION_LISTEN=0.0.0.0:8088 MPX_PROVISION_DATA=/data/tunnels.json
EXPOSE 8088
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/mpx-provision"]
