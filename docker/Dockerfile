FROM golang:1.15-alpine as builder

WORKDIR /go/src/app/

# We require Bash for executing the build script
RUN apk add --update bash

# Fill cache with source code of dependencies
COPY go.mod go.sum ./
RUN go mod download -x

COPY . .
RUN scripts/build.sh linux

FROM gcr.io/distroless/static

COPY --from=builder /go/src/app/deflix-stremio /
COPY --from=builder /go/src/app/web/configure /web/configure

# Default bind addr is localhost, which wouldn't allow connections from outside the container.
# Should be overwritten when using `--network host` and not wanting to expose the service to other hosts.
ENV BIND_ADDR 0.0.0.0

# distroless/static `os.UserCacheDir()` leads to "/root/.cache", so the persisted cache will be in "/root/.cache/deflix-stremio/"
# Using a proper volume makes the data accessible outside the container and is apparently faster.
VOLUME [ "/root/.cache/deflix-stremio/" ]
VOLUME [ "/web/configure/" ]
EXPOSE 8080

# Using ENTRYPOINT instead of CMD allows the user to easily just *add* command line arguments when using `docker run`
ENTRYPOINT ["/deflix-stremio"]
