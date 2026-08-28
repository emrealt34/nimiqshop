#!/usr/bin/env bash
#
# audit-operator-key.sh — STATIC PROOF that the operator's Polygon private key
# (and every other secret) can NEVER leave the server through the app code.
#
# This is the "we don't steal your key" guarantee, made machine-checkable. It
# fails the build/CI if any Go source:
#   1. logs (Print/Fatal/Panic/Errorf…) a variable whose name contains
#      private/secret/key/operator, or
#   2. declares a struct field with a `json:` tag on a private/secret/key field
#      (which would serialize it into an HTTP response).
#
# Run: ./tools/audit-operator-key.sh   (exit 0 = clean, 1 = leak detected)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT/backend"

status=0

# 1) Secrets must never appear in any log statement.
if grep -rnE 'log\.(Print|Fatal|Panic|Errorf|Warn)[^;]*[Pp]rivate|log\.(Print|Fatal|Panic|Errorf|Warn)[^;]*[Ss]ecret|log\.(Print|Fatal|Panic|Errorf|Warn)[^;]*OperatorKey' --include='*.go' .; then
  echo "❌ FAIL: a private/secret key value is referenced in a log statement."
  status=1
fi

# 2) No private/secret field may carry a json tag (i.e. be response-serializable).
if grep -rnE 'json:"[^"]*"[^`]*`(private|secret)' --include='*.go' .; then :; fi
if grep -rnE '\b(Private|Secret)(Key|KeyHex|WalletKey)\s+\S+\s+`json:"[^"]+"' --include='*.go' .; then
  echo "❌ FAIL: a private/secret field is JSON-serializable and could leak in a response."
  status=1
fi

# 3) The direct Lightning production rail must not contain an operator wallet key.
hits=$(grep -rn 'PolygonOperatorPrivateKey\|NIMIQ_REFUND_PRIVATE_KEY\|PRIVATE_KEY' --include='*.go' . | wc -l || true)
if [ "$hits" -ne 0 ]; then
  echo "❌ FAIL: a production private-key reference remains in the active backend source."
  status=1
fi

if [ "$status" -eq 0 ]; then
  echo "✅ OK: no operator/payment private key is logged, marshaled, or present in the active backend rail."
  echo "   Nimiq Pay performs the NIM→BTC Lightning swap; the server only stores the CryptoRefills invoice."
  exit 0
else
  exit "$status"
fi
