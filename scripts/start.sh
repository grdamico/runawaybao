
#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

docker compose up -d

echo "Waiting for OpenBao..."

for attempt in $(seq 1 20); do
  if curl --silent --fail \
    http://127.0.0.1:8200/v1/sys/health >/dev/null; then
    echo "OpenBao is responding."
    docker compose ps
    exit 0
  fi

  sleep 1
done

echo "OpenBao did not become ready."
echo "Check logs with: docker compose logs openbao"
exit 1