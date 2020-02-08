FROM golang:1.13-alpine as builder

WORKDIR /go/src/app
COPY . /go/src/app

# Witghout disabling CGO the binary doesn't run in distroless/base
RUN CGO_ENABLED=0 go build -v -ldflags="-s -w"

FROM gcr.io/distroless/base

COPY --from=builder /go/src/app/deflix-stremio /

# distroless/base `os.UserCacheDir()` leads to "/root/.cache", so the persisted cache will be in "/root/.cache/deflix-stremio/"
# Using a proper volume makes the data accessible outside the container and is apparently faster.
VOLUME [ "/root/.cache/deflix-stremio" ]
EXPOSE 8080

ENTRYPOINT ["/deflix-stremio"]
