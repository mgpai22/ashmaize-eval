#!/usr/bin/env bash
# Verify the candidate container's affordance boundary:
#   1. the prebuilt oracle answers a valid request,
#   2. the spec is readable,
#   3. AshMaize source / scenarios / grader are ABSENT from the agent's view, and
#   4. the network is denied under `--network none`.
#
# Usage: container/check_offline.sh [image-tag]   (default: ashmaize-eval)
set -euo pipefail
IMG="${1:-ashmaize-eval}"
NET=(--network none)
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "[1] oracle responds to a valid rom_digest request"
out=$(echo '{"op":"rom_digest","rom_seed_hex":"313233","rom_size":4096}' \
        | docker run --rm -i "${NET[@]}" "$IMG" oracle)
echo "    -> $out"
echo "$out" | grep -q '"rom_digest_hex"' || fail "oracle did not return rom_digest_hex"

echo "[2] spec is present and readable"
docker run --rm "${NET[@]}" "$IMG" sh -c \
  'test -r /task/spec/TASK.md && test -r /task/spec/ABI.md && test -r /task/spec/ASHMAIZE.md' \
  || fail "spec files missing/unreadable"
echo "    -> spec/{TASK,ABI,ASHMAIZE}.md readable"

echo "[3] only the spec is exposed under /task (no source/scenarios/grader)"
files=$(docker run --rm "${NET[@]}" "$IMG" sh -c 'find /task -type f | sort')
echo "$files" | sed 's/^/    /'
if echo "$files" | grep -Eiq 'ce-ashmaize|scenarios|grader|\.rs$|cargo\.toml|main\.go'; then
  fail "forbidden artifact present under /task"
fi
[ "$(echo "$files" | wc -l)" -eq 3 ] || fail "expected exactly the 3 spec files under /task"

echo "[4] network is denied (probe must fail)"
probe=$(docker run --rm "${NET[@]}" "$IMG" python3 -c \
  'import socket,sys
try:
    socket.setdefaulttimeout(5); socket.create_connection(("1.1.1.1",53)); print("REACHABLE")
except OSError as e:
    print("blocked")' )
echo "    -> $probe"
[ "$probe" = "blocked" ] || fail "network was reachable under --network none"

echo "ALL OFFLINE AFFORDANCE CHECKS PASSED"
