#!/usr/bin/env bash
# Unit checks for InnoDB buffer pool rounding (no MySQL required).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/provisioning/lib/mysql-pool-math.sh"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

got="$(round_innodb_buffer_pool_mb_up 2560)"
[[ "$got" == "3072" ]] || fail "2560M should round up to 3072M, got ${got}M"

got="$(round_innodb_buffer_pool_mb_up 2048)"
[[ "$got" == "2048" ]] || fail "2048M should stay 2048M, got ${got}M"

got="$(round_innodb_buffer_pool_mb_up 512)"
[[ "$got" == "512" ]] || fail "512M (<1G, 1 instance) should stay 512M, got ${got}M"

got="$(round_innodb_buffer_pool_mb_up 600)"
[[ "$got" == "640" ]] || fail "600M should round up to 640M (128×1), got ${got}M"

got="$(round_innodb_buffer_pool_mb_up 641)"
[[ "$got" == "768" ]] || fail "641M should round up to 768M (128×1), got ${got}M"

echo "OK: mysql-pool-math"
