#!/usr/bin/env bash
set -euo pipefail
mkdir -p dist
export CGO_ENABLED=1
go mod tidy
go build -trimpath -ldflags "-s -w" -o dist/socksrevivepc ./cmd/socksrevivepc
echo "Built dist/socksrevivepc"
echo "Run with sudo when using TUN mode."
