#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"
./scripts/status.sh >/dev/null
echo "Watching process events. Launch the README test workload in another terminal."
echo "Press Ctrl+C to stop listening."
dc exec -T --interactive=false tetragon tetra getevents -o compact 2>&1 \
  | grep --line-buffered -iE 'alpine|/bin/sh -c.*sleep|/bin/sleep|runawaybao-observation'
