#!/usr/bin/env bash
# One launcher for every platform; tunnel identity is configured automatically.
set -euo pipefail
cd "$(dirname "$0")"
exec node scripts/run-stack.mjs "${1:-preview}"
