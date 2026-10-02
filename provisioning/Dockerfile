FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/mpx-provision .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mpx-provision /usr/local/bin/mpx-provision
ENV MPX_PROVISION_LISTEN=0.0.0.0:8088
ENV MPX_PROVISION_DATA=/data/profiles.json
VOLUME ["/data"]
EXPOSE 8088
ENTRYPOINT ["/usr/local/bin/mpx-provision"]
