#!/usr/bin/env bash
# Production build: garble -literals + -seed=random + controlflow + trimpath.
# Produces dist/AppService.exe for H6 hardening.
set -euo pipefail

cd "$(dirname "$0")/.."

export GOGARBLE='*,!local/internal/svc/internal/server*,!local/internal/svc/internal/event*,!github.com/inkeliz/gowebview*'
export GARBLE_EXPERIMENTAL_CONTROLFLOW=1

mkdir -p dist
BUILD_ID=$(powershell -Command "[guid]::NewGuid().ToString()" 2>/dev/null | tr -d '\r' || echo "$(date +%s)-$$")
BUILD_TIME=$(date -Iseconds)

echo "Building dist/AppService.exe..."
echo "  BUILD_ID=$BUILD_ID"
echo "  BUILD_TIME=$BUILD_TIME"
echo "  GOGARBLE=$GOGARBLE"

garble -seed=random -literals build -a -trimpath -tags static \
  -ldflags "-s -w -H windowsgui -X 'main._bMeta0=$BUILD_ID' -X 'main._bMeta1=$BUILD_TIME' -X 'local/internal/svc/internal/config.Version=dev'" \
  -o "dist/AppService.exe" ./cmd/app

echo ""
ls -la dist/AppService.exe
echo ""
echo "Running leak audit..."
python tools/audit_binary_leaks.py dist/AppService.exe
