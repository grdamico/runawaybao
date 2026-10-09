
#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
set -a
source .env
set +a

bao_json='{"data":{"username":"lab-user","password":"not-a-real-password"}}'

curl --silent --show-error --fail \
  --header "X-Vault-Token: ${BAO_TOKEN}" \
  --header "Content-Type: application/json" \
  --request POST \
  --data "${bao_json}" \
  "${BAO_ADDR}/v1/secret/data/demo"

echo
echo "Demo secret saved."
