#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
dc ps
for service in openbao tetragon; do
  id=$(dc ps --all --quiet "$service")
  if [[ -z "$id" ]] || [[ $(docker inspect --format '{{.State.Running}} {{index .Config.Labels "com.docker.compose.project"}}' "$id") != 'true runawaybao' ]]; then
    echo "$service is unavailable in project runawaybao." >&2
    exit 1
  fi
  health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id")
  if [[ "$health" != none && "$health" != healthy ]]; then
    echo "$service has Docker health status: $health" >&2
    exit 1
  fi
done
# Probe inside the selected service, never an unrelated host-port listener.
dc exec -T openbao bao status -address=http://127.0.0.1:8200 >/dev/null
dc exec -T openbao wget -q -O /dev/null http://127.0.0.1:8200/v1/sys/health
dc exec -T tetragon tetra --timeout 2s status >/dev/null
echo "OpenBao and Tetragon are healthy (project runawaybao)."
