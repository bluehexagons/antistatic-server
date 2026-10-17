#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"

npm run --silent check
go test -race -p 2 -parallel 2 -timeout 2m ./...
go vet -p 2 ./...
CGO_ENABLED=0 go build -p 2 ./...
