#!/usr/bin/env bash
# Bootstrap credentials remain in environment/process memory, never arguments.
set +x
source "$(dirname "$0")/common.sh"
dc up -d openbao postgres
for ((attempt=1; attempt<=60; attempt++)); do
  if dc exec -T postgres pg_isready -h 127.0.0.1 -U runawaybao_admin -d runawaybao >/dev/null 2>&1 &&
     dc exec -T openbao wget -q -O /dev/null http://127.0.0.1:8200/v1/sys/health; then
    break
  fi
  sleep 1
done
if ((attempt>60)); then
  echo "Database lab did not become ready." >&2
  exit 1
fi
export RUNAWAYBAO_DATABASE_INTEGRATION=1
export RUNAWAYBAO_DATABASE_ADDR="$BAO_ADDR"
export RUNAWAYBAO_DATABASE_ROOT_TOKEN="$BAO_TOKEN"
export RUNAWAYBAO_DATABASE_PORT="${POSTGRES_PORT:-15432}"
export RUNAWAYBAO_DATABASE_MANAGER_PASSWORD="${DB_MANAGER_PASSWORD:-runawaybao-manager-dev-only}"
go test -tags=integration ./databasejob -run '^TestLiveDatabase$' -count=1 -v
