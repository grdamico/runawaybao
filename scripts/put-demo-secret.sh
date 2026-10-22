#!/usr/bin/env bash
source "$(dirname "$0")/common.sh"

bao_json='{"data":{"username":"lab-user","password":"not-a-real-password"}}'

curl --silent --show-error --fail --connect-timeout 5 --max-time 15 \
  --header "X-Vault-Token: ${BAO_TOKEN}" \
  --header "Content-Type: application/json" \
  --request POST \
  --data "${bao_json}" \
  "${BAO_ADDR}/v1/secret/data/demo"

echo
echo "Demo secret saved."
