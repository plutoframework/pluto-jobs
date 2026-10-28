#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go test -run '^$' -fuzz '^FuzzRequest$' -fuzztime=5s -parallel=2 .
