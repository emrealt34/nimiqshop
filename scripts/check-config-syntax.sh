#!/usr/bin/env bash
# Syntax gate for the files GitHub and the release tooling parse but the
# TypeScript/Go compilers never see:
#
#   *.mjs   node --check  (the dev/build/tunnel scripts)
#   *.json  JSON.parse    (package.json, tsconfig, oxlintrc, playwright, …)
#   *.py    compileall    (scripts/*.py, devtools helpers)
#
# A broken brace in a script only shows up as a confusing runtime failure
# halfway through a release; this fails in two seconds instead.
#
#   ./scripts/check-config-syntax.sh
set -euo pipefail

cd "$(dirname "$0")/.."

status=0

# `git ls-files` keeps the list to tracked files, so a stray editor backup or
# a build artifact can never turn this into a red CI run.
tracked() {
  git ls-files "$1" 2>/dev/null || true
}

echo "==> .mjs"
while IFS= read -r file; do
  [ -n "$file" ] || continue
  if ! node --check "$file" >/dev/null; then
    echo "::error file=$file::JavaScript syntax error"
    status=1
  fi
done < <(tracked '*.mjs')

echo "==> .json"
while IFS= read -r file; do
  [ -n "$file" ] || continue
  if ! node -e "JSON.parse(require('fs').readFileSync(process.argv[1],'utf8'))" "$file"; then
    echo "::error file=$file::invalid JSON"
    status=1
  fi
done < <(tracked '*.json')

echo "==> .py"
if command -v python3 >/dev/null 2>&1; then
  pyfiles=()
  while IFS= read -r file; do
    [ -n "$file" ] || continue
    pyfiles+=("$file")
  done < <(tracked '*.py')
  if [ ${#pyfiles[@]} -gt 0 ]; then
    if ! python3 -m py_compile "${pyfiles[@]}"; then
      echo "::error::Python syntax error in the files above"
      status=1
    fi
    find . -name '__pycache__' -type d -prune -exec rm -rf {} + 2>/dev/null || true
  fi
else
  echo "python3 not available — skipped"
fi

if [ "$status" -ne 0 ]; then
  echo "config syntax check failed"
  exit 1
fi
echo "config syntax check passed"
