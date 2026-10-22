#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"

curl --silent --show-error --fail --connect-timeout 5 --max-time 15 \
  --header "X-Vault-Token: ${BAO_TOKEN}" \
  "${BAO_ADDR}/v1/secret/data/demo"

echo
