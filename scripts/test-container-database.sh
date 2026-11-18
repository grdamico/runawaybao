#!/usr/bin/env bash
# Disposable trusted bootstrap; credentials never passed to worker Docker config.
set +x
source "$(dirname "$0")/common.sh"
dc up -d openbao postgres tetragon
for ((attempt=1; attempt<=60; attempt++)); do
  if ./scripts/status.sh >/dev/null 2>&1; then break; fi
  sleep 1
done
if ((attempt>60)); then echo "Runtime lab did not become ready." >&2; exit 1; fi
docker build -f worker/Dockerfile --build-arg GO_VERSION="$(awk '$1=="go" {print $2}' go.mod)" -t runawaybao-db-worker:local . >/dev/null
export RUNAWAYBAO_CONTAINER_DATABASE_INTEGRATION=1
export RUNAWAYBAO_DATABASE_ADDR="$BAO_ADDR"
export RUNAWAYBAO_DATABASE_ROOT_TOKEN="$BAO_TOKEN"
export RUNAWAYBAO_DATABASE_PORT="${POSTGRES_PORT:-15432}"
export RUNAWAYBAO_DATABASE_MANAGER_PASSWORD="${DB_MANAGER_PASSWORD:-runawaybao-manager-dev-only}"
export RUNAWAYBAO_DATABASE_ADMIN_PASSWORD="${POSTGRES_PASSWORD:-runawaybao-admin-dev-only}"
go test -tags=integration ./databasejob -run '^TestLiveContainerDatabase$' -count=1 -timeout 5m -v
