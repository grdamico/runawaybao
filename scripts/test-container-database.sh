#!/usr/bin/env bash
# Disposable trusted bootstrap; credentials never passed to worker Docker config.
set +x
source "$(dirname "$0")/common.sh"
test_name=TestLiveContainerDatabase
test_timeout=5m
case "${1:-}" in
  "") ;;
  identity)
    if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
      echo "Live workload identity validation is local only." >&2
      exit 1
    fi
    export RUNAWAYBAO_IDENTITY_INTEGRATION=1
    test_name=TestLiveWorkloadIdentity
    ;;
  enforce)
    if [[ -n "${GITHUB_ACTIONS:-}" || ! -t 0 || $# != 2 || "$2" != /* ]]; then
      echo "Enforcement requires a local terminal and an absolute protected journal path." >&2
      exit 1
    fi
    export RUNAWAYBAO_ENFORCEMENT_INTEGRATION=1
    export RUNAWAYBAO_ENFORCEMENT_JOURNAL="$2"
    test_name=TestLiveControlledEnforcement
    test_timeout=8m
    ;;
  *) echo "Usage: $0 [identity | enforce JOURNAL]" >&2; exit 1 ;;
esac
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
go test -tags=integration ./databasejob -run "^${test_name}$" -count=1 -timeout "$test_timeout" -v
