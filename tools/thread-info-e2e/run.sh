#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)
alloy_bin=${ALLOY_BIN:-$repo_dir/build/alloy-linux-amd64}
alloy_bin=$(realpath -- "$alloy_bin")
test_dir=$(mktemp -d /tmp/alloy-thread-e2e.XXXXXXXX)
export ALLOY_E2E_TMP=$test_dir/asprof
mkdir -p "$ALLOY_E2E_TMP"
echo "E2E artifacts: $test_dir"

cd -- "$repo_dir"
go build -mod=mod -o "$test_dir/profilecapture" ./cmd/profilecapture
javac -d "$test_dir" tools/thread-info-e2e/AlloyThreadE2E.java
java -cp "$test_dir" AlloyThreadE2E >"$test_dir/java.log" 2>&1 &
java_pid=$!
capture_pid=
cleanup() {
    kill "$java_pid" ${capture_pid:+"$capture_pid"} 2>/dev/null || true
    wait "$java_pid" ${capture_pid:+"$capture_pid"} 2>/dev/null || true
}
trap cleanup EXIT

for mode in frame-label label-only; do
    "$test_dir/profilecapture" -output "$test_dir/$mode-profiles" >"$test_dir/$mode-receiver.log" 2>&1 &
    capture_pid=$!
    sleep 1
    kill -0 "$capture_pid"
    "$alloy_bin" validate "tools/thread-info-e2e/$mode.alloy"
    timeout --signal=TERM 14s "$alloy_bin" run \
        --storage.path="$test_dir/$mode-data" "tools/thread-info-e2e/$mode.alloy" \
        >"$test_dir/$mode.log" 2>&1 || test "$?" -eq 124
    kill "$capture_pid"
    wait "$capture_pid" 2>/dev/null || true
    capture_pid=
    if ! compgen -G "$test_dir/$mode-profiles/*.pprof" >/dev/null; then
        echo "No profiles captured for $mode; inspect $test_dir/$mode.log" >&2
        exit 1
    fi
done
echo "Profiles captured in both modes. Inspect thread_name sample labels and root frames."
