#!/bin/sh
# Reproducible, credential-free local verification. Docker is verified separately.
set -eu
cd "$(dirname "$0")/.."
unformatted=$(gofmt -l $(rg --files -g '*.go' -g '!vendor/**'))
if [ -n "$unformatted" ]; then
  printf '%s\n' "$unformatted"
  exit 1
fi
go mod verify
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/email-invoice-guard-linux-amd64 ./cmd/server
