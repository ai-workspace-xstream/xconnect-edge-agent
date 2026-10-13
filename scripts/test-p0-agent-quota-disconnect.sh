#!/usr/bin/env bash
# P0-A regression gate. This is local/CI-only: it never connects to UAT or
# restarts a local Caddy/Xray service.
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

go test -count=1 ./internal/agentmode ./internal/xrayconfig
go vet ./...

if [[ "${P0_SESSION_EXTENSION:-0}" == "1" ]]; then
  : "${XRAY_SESSION_TEST_BINARY:?Set the absolute maintained Xray binary path}"
  test -x "$XRAY_SESSION_TEST_BINARY"
  go test -tags xrayintegration -count=1 ./internal/xrayconfig -run TestExtended -v
fi

if [[ "${P0_RACE:-0}" == "1" ]]; then
  go test -race -count=1 ./internal/agentmode ./internal/xrayconfig
fi
