#!/usr/bin/env bash
# Explicit development-only integration test. Never enable shell tracing here.
set +x
source "$(dirname "$0")/common.sh"
export RUNAWAYBAO_ACL_INTEGRATION=1
export RUNAWAYBAO_ACL_ADDR="$BAO_ADDR"
export RUNAWAYBAO_ACL_ROOT_TOKEN="$BAO_TOKEN"
go test -tags=integration ./openbao -run '^TestLiveACL$' -count=1 -v
