#!/usr/bin/env bash
# Presentation only: all checks and cleanup stay in the existing live suites.
set +x
set -euo pipefail
cd "$(dirname "$0")/.."

usage() {
  printf 'Usage: ./scripts/demo.sh {credentials|correlation|policy|container-database|all}\n'
  printf 'Requires disposable local configuration; see docs/demo.md.\n'
}

case ${1:-} in
  -h|--help) usage; exit 0 ;;
  credentials|correlation|policy|container-database|all) mode=$1 ;;
  *) usage >&2; exit 2 ;;
esac
if (($# != 1)); then usage >&2; exit 2; fi
trap 'printf "Demo stopped: a check failed.\n" >&2' ERR

if [[ "$mode" == credentials || "$mode" == all ]]; then
  printf '\n=== A. Credential isolation: explicit revocation ===\n'
  printf 'Verify unique A/B credentials, deny unauthorized SQL, revoke A, and keep B working.\n'
  ./scripts/test-database.sh
  printf 'Credential isolation checks passed.\n'
fi

if [[ "$mode" == correlation || "$mode" == all ]]; then
  printf '\n=== B. Runtime correlation: read-only observation ===\n'
  printf 'Observe restricted A/B workloads; correlate identity; keep both database credentials working.\n'
  printf 'No runtime event requests credential revocation. Cleanup waits for natural expiry.\n'
  ./scripts/test-runtime-correlation.sh
  printf 'Read-only correlation checks passed.\n'
fi

if [[ "$mode" == policy ]]; then
  printf '\n=== C. Runtime policy evaluation: dry-run only ===\n'
  printf 'Compare explicit per-workload allowlists; no security enforcement occurs.\n'
  ./scripts/test-runtime-policy.sh
  printf 'Dry-run policy checks passed.\n'
fi

if [[ "$mode" == container-database ]]; then
  printf '\n=== D. Containerized database workloads: dry-run only ===\n'
  ./scripts/test-container-database.sh
  printf 'Containerized database checks passed.\n'
fi

printf '\nSelected live demonstrations passed. Project services remain running.\n'
