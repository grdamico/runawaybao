#!/usr/bin/env bash
# Trusted bootstrap harness only; no token is passed to observer subprocesses.
set +x
source "$(dirname "$0")/common.sh"
dc up -d openbao postgres tetragon
for ((attempt=1; attempt<=60; attempt++)); do
  if ./scripts/status.sh >/dev/null 2>&1; then break; fi
  sleep 1
done
if ((attempt>60)); then echo "Runtime lab did not become ready." >&2; exit 1; fi
docker pull alpine:3.22 >/dev/null
export RUNAWAYBAO_RUNTIME_INTEGRATION=1
export RUNAWAYBAO_DATABASE_ADDR="$BAO_ADDR"
export RUNAWAYBAO_DATABASE_ROOT_TOKEN="$BAO_TOKEN"
export RUNAWAYBAO_DATABASE_PORT="${POSTGRES_PORT:-15432}"
export RUNAWAYBAO_DATABASE_MANAGER_PASSWORD="${DB_MANAGER_PASSWORD:-runawaybao-manager-dev-only}"
go test -tags=integration ./databasejob -run '^TestLiveRuntime$' -count=1 -timeout 5m -v
