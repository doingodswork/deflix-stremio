#!/bin/bash

# This script builds deflix-stremio.
# It requires Go to be installed already.
# It doesn't matter what the working directory is when calling this script.

set -euxo pipefail
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"

if [ "$#" -eq 0 ]; then
    echo "You have to pass a target operating system as argument, like windows, darwin or linux"
    exit 1
fi

# Create pkger files
cd "${DIR}/.."
go run github.com/markbates/pkger/cmd/pkger
sed -i "s/package .*/package main/" pkged.go
mv pkged.go cmd/deflix-stremio/

# Compile
# Without disabling CGO the binary doesn't run in distroless/static
CGO_ENABLED=0 GOOS="$1" go build -v -ldflags="-s -w" ./cmd/deflix-stremio/
