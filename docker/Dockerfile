FROM golang:1.13-alpine as builder

WORKDIR /go/src/app
COPY . /go/src/app

# Without disabling CGO the binary doesn't run in distroless/static
RUN CGO_ENABLED=0 go build -v -ldflags="-s -w"

FROM gcr.io/distroless/static

COPY --from=builder /go/src/app/deflix-stremio /

# Default bind addr is localhost, which wouldn't allow connections from outside the container.
# Should be overwritten when using `--network host` and not wanting to expose the service to other hosts.
ENV BIND_ADDR 0.0.0.0

# distroless/static `os.UserCacheDir()` leads to "/root/.cache", so the persisted cache will be in "/root/.cache/deflix-stremio/"
# Using a proper volume makes the data accessible outside the container and is apparently faster.
VOLUME [ "/root/.cache/deflix-stremio" ]
EXPOSE 8080

# Using ENTRYPOINT instead of CMD allows the user to easily just *add* command line arguments when using `docker run`
ENTRYPOINT ["/deflix-stremio"]
