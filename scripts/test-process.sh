
#!/usr/bin/env bash
set -euo pipefail

if ! docker inspect -f '{{.State.Running}}' tetragon 2>/dev/null \
  | grep -qx true; then
  echo "Tetragon is not running. Run ./scripts/start.sh first."
  exit 1
fi

echo "Starting a test process in Alpine..."
echo "Press Ctrl+C to stop watching events."
echo

docker exec tetragon tetra getevents -o compact 2>&1 \
  | grep --line-buffered -iE 'alpine|/bin/sh -c.*sleep|/bin/sleep'
