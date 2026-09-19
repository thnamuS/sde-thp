#!/bin/sh
set -eu
API_URL="${API_URL:-http://localhost:8080}"
ACME_URL="${ACME_URL:-http://localhost:8090}"

curl -fsS "$API_URL/readyz"
curl -fsS "$ACME_URL/healthz"
response="$(curl -fsS "$API_URL/v1/auth/login" -H 'Content-Type: application/json' -d '{"email":"ops@acme.test","password":"AcmeDemo123!"}')"
token="$(printf '%s' "$response" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
test -n "$token"
curl -fsS "$API_URL/v1/operations" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-$(date +%s)" -d '{"task_type":"acme.ticket.process","execution_mode":"PRIVATE","target_url":"http://acme:8090/work","payload":{"mode":"normal"}}'
sleep 3
curl -fsS "$API_URL/v1/operations" -H "Authorization: Bearer $token"
printf '\nSmoke test passed.\n'
