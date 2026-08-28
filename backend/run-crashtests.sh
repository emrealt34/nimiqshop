#!/usr/bin/env bash
# Run every destructive crash scenario as its OWN compiled-test process.
#
# Speed strategy (vs. the old serial runner):
#   1. The server, mockstack and the crashtest binary are built ONCE up
#      front — no per-case `go build`/`go test` compile overhead.
#   2. All scenarios run IN PARALLEL. Each scenario uses a fresh temp dir
#      and grabs its own free ports (see crashtest.freePort), so they are
#      fully independent and safe to overlap.
#   3. Per-case hard timeouts still identify exactly which scenario stalls.
#
# Wall time ≈ the single slowest scenario instead of the sum of all of them.
# Set CRASH_SERIAL=1 to fall back to one-at-a-time execution.
set -euo pipefail

GO_BIN="${GO_BIN:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "Go was not found. Set GO_BIN=/path/to/go or add Go to PATH." >&2
  exit 127
fi

BIN_DIR="$(mktemp -d /tmp/nimshop-crashbin.XXXXXX)"
LOG_DIR="$BIN_DIR/logs"
mkdir -p "$LOG_DIR"
trap 'rm -rf "$BIN_DIR"' EXIT

echo "==> Building binaries once into $BIN_DIR"
"$GO_BIN" build -o "$BIN_DIR/mockstack" ./cmd/mockstack
"$GO_BIN" build -o "$BIN_DIR/nimshop-server" ./cmd/server
"$GO_BIN" test -c -o "$BIN_DIR/crashtest.bin" ./crashtest/

export CRASH_MOCK_BIN="$BIN_DIR/mockstack"
export CRASH_SERVER_BIN="$BIN_DIR/nimshop-server"

run_case() {
  local name="$1" limit="$2"
  local log="$LOG_DIR/$name.log"
  # --foreground lets the test binary receive SIGTERM and execute TestMain
  # cleanup instead of silently abandoning mockstack/server children on a
  # deadline.
  timeout --foreground --kill-after=10s "$limit" \
    "$BIN_DIR/crashtest.bin" -test.run "^${name}$" -test.v -test.timeout="$limit" \
    >"$log" 2>&1
}

CASES=(
  "TestC01_GarbageStorm 45s"
  # Firehose margin: most calls fail fast (429) under the deliberately tight
  # partner-account budget; the slow path is a 20s supplier-call context on an
  # admitted job, plus the drain wait inside the test.
  "TestC02_ConcurrentFirehose 4m"
  "TestC03_QueueExhaustion 3m"
  "TestC04_KillMidOrder 2m"
  "TestC05_WebhookFlood 2m"
  "TestC06_DBCorrupt 75s"
  "TestC07_RestartStorm 3m"
  "TestC08_HappyPath 2m"
  "TestC09_BeneficiaryRules 2m"
)

START_TS=$SECONDS
FAIL=0

if [ "${CRASH_SERIAL:-0}" = "1" ]; then
  for entry in "${CASES[@]}"; do
    name="${entry% *}"; limit="${entry#* }"
    echo
    echo "========== ${name} (hard limit ${limit}) =========="
    if ! run_case "$name" "$limit"; then
      FAIL=1
      echo "FAILED: ${name}"; tail -n 40 "$LOG_DIR/$name.log"
    else
      echo "PASS: ${name}"
    fi
  done
else
  echo "==> Running ${#CASES[@]} crash scenarios in parallel"
  declare -A PIDS
  for entry in "${CASES[@]}"; do
    name="${entry% *}"; limit="${entry#* }"
    run_case "$name" "$limit" &
    PIDS["$name"]=$!
  done
  for name in "${!PIDS[@]}"; do
    if wait "${PIDS[$name]}"; then
      echo "PASS: $name"
    else
      FAIL=1
      echo "FAILED: $name — last 40 log lines:"
      tail -n 40 "$LOG_DIR/$name.log"
    fi
  done
fi

ELAPSED=$((SECONDS - START_TS))
if [ "$FAIL" -ne 0 ]; then
  echo
  echo "Crash suite FAILED after ${ELAPSED}s. Full logs in $LOG_DIR"
  trap - EXIT
  exit 1
fi
echo
echo "All isolated crash tests passed in ${ELAPSED}s."
