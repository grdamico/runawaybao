#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
dc up -d
for ((attempt=1; attempt<=30; attempt++)); do
  if ./scripts/status.sh >/dev/null 2>&1; then
    ./scripts/status.sh
    exit 0
  fi
  sleep 1
done
echo "Services did not become ready. Check: docker compose -p runawaybao logs" >&2
exit 1
