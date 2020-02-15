FROM golang:1.13-alpine as builder

WORKDIR /go/src/app
COPY . /go/src/app

# Without disabling CGO the binary doesn't run in distroless/base
RUN CGO_ENABLED=0 go build -v -ldflags="-s -w"

FROM gcr.io/distroless/base

COPY --from=builder /go/src/app/deflix-stremio /

# Default bind addr is localhost, which wouldn't allow connections from outside the container
ENV BIND_ADDR 0.0.0.0

# distroless/base `os.UserCacheDir()` leads to "/root/.cache", so the persisted cache will be in "/root/.cache/deflix-stremio/"
# Using a proper volume makes the data accessible outside the container and is apparently faster.
VOLUME [ "/root/.cache/deflix-stremio" ]
EXPOSE 8080

# CLI arguments can't be used here because the Docker image doesn't contain a shell.
# That's also why we use CMD instead of ENTRYPOINT (because otherwise a user might think he can add CLI arguments in `docker run`).
CMD ["/deflix-stremio"]
