#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

docker compose ps

echo
echo "OpenBao health:"
curl --silent --show-error \
  http://127.0.0.1:8200/v1/sys/health || true
echo
