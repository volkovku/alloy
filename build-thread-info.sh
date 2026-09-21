#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
export GOTOOLCHAIN=go1.26.5
export GOWORK=off
git submodule update --init third_party/thread-info-jfr-parser
GOFLAGS='-mod=mod -p=2' make alloy CGO_ENABLED=0 RELEASE_BUILD=1 \
    SKIP_UI_BUILD=1 SKIP_CODE_GENERATION=1 GOOS=linux GOARCH=amd64 GO_TAGS='' \
    ALLOY_BINARY=build/alloy-linux-amd64 \
    VERSION=v1.17.0-devel-pr6725-100cfcc-parser116-b18082a
