#!/usr/bin/env bash
# Shared by the lab scripts; source rather than execute.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [[ ! -f .env ]]; then
  echo "Missing .env. Run: cp .env.example .env" >&2
  exit 1
fi
set -a
source .env
set +a
if [[ -z ${BAO_TOKEN:-} ]]; then
  echo "Missing BAO_TOKEN in .env; use development-only values from .env.example." >&2
  exit 1
fi
BAO_PORT=${BAO_PORT:-8200}
BAO_ADDR="http://127.0.0.1:${BAO_PORT}"
# Explicit project/file prevent ambient Compose settings selecting another lab.
dc() { docker compose --project-name runawaybao --file "$PWD/compose.yaml" --env-file "$PWD/.env" "$@"; }
