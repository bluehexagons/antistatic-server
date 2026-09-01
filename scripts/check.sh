#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

npm run check
go test -race ./...
go vet ./...
CGO_ENABLED=0 go build ./...
