#!/usr/bin/env bash
# POST /api/initialize (cold caches), then loadbench; capture separate CPU profiles for
# cold-start and warmed-up windows. For validating user-fetch / fillUsersByIDs vs production baseline.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO_DIR="$ROOT/go"
OUT_DIR="${OUT_DIR:-/tmp/isucon-init-profile}"
BIN="${TMPDIR:-/tmp}/isupipe-init-profile"
COLD_SEC="${COLD_PROFILE_SECONDS:-20}"
WARM_SEC="${WARM_PROFILE_SECONDS:-30}"
WARM_WAIT="${WARM_WAIT_SECONDS:-75}"
LOAD_DURATION="${LOAD_DURATION:-150s}"
LOAD_WORKERS="${LOAD_WORKERS:-80}"

mkdir -p "$OUT_DIR"

# init.sh ends with pdnsutil; stub for local dev without PowerDNS (must be on server PATH).
FAKE_PDNS="${OUT_DIR}/fakebin"
mkdir -p "$FAKE_PDNS"
printf '#!/bin/sh\nexit 0\n' >"$FAKE_PDNS/pdnsutil"
chmod +x "$FAKE_PDNS/pdnsutil"
export PATH="$FAKE_PDNS:$PATH"

if ! mysqladmin ping -h127.0.0.1 -uisucon -pisucon &>/dev/null; then
	echo "MySQL not reachable" >&2
	exit 1
fi

LS_ROW="$(mysql -h127.0.0.1 -uisucon -pisucon -N isupipe -e "
SELECT l.id, u.name FROM livestreams l
JOIN users u ON u.id = l.user_id
INNER JOIN livecomments lc ON lc.livestream_id = l.id
GROUP BY l.id, u.name ORDER BY COUNT(*) DESC LIMIT 1;" 2>/dev/null)"
read -r LSID STREAMER <<<"$LS_ROW"

echo "Building..."
(cd "$GO_DIR" && go build -o "$BIN" .)

if curl -sf "http://127.0.0.1:8080/api/tag" -H "Host: u.isucon.local" >/dev/null 2>&1; then
	echo "Using app on :8080"
else
	echo "Starting app (cwd=$GO_DIR for ../img paths)..."
	( cd "$GO_DIR" && PATH="$FAKE_PDNS:$PATH" ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=127.0.0.1 "$BIN" ) >/tmp/isupipe-init-profile.log 2>&1 &
	APP_PID=$!
	trap 'kill "$APP_PID" 2>/dev/null || true' EXIT
	sleep 2
fi

echo "POST /api/initialize (wipe tags + process caches)..."
curl -sS -X POST "http://127.0.0.1:8080/api/initialize" -H "Host: u.isucon.local" \
	-o "$OUT_DIR/initialize.json" -w "initialize_http=%{http_code}\n"

echo "Starting loadbench immediately (workers=$LOAD_WORKERS duration=$LOAD_DURATION)..."
(
	cd "$GO_DIR"
	LOAD_WORKERS="$LOAD_WORKERS" LOAD_DURATION="$LOAD_DURATION" \
		LOAD_LIVESTREAM_ID="$LSID" LOAD_STREAMER="$STREAMER" \
		go run ./cmd/loadbench
) &
LOAD_PID=$!

echo "COLD window: CPU profile ${COLD_SEC}s (t=0 after initialize)..."
curl -sS -o "$OUT_DIR/cpu-cold.prof" "http://127.0.0.1:6060/debug/pprof/profile?seconds=${COLD_SEC}"

echo "WARM_WAIT ${WARM_WAIT}s of continued load..."
sleep "$WARM_WAIT"

echo "WARM window: CPU profile ${WARM_SEC}s..."
curl -sS -o "$OUT_DIR/cpu-warm.prof" "http://127.0.0.1:6060/debug/pprof/profile?seconds=${WARM_SEC}"

wait "$LOAD_PID" 2>/dev/null || true

echo "Profiles: $OUT_DIR/cpu-cold.prof $OUT_DIR/cpu-warm.prof"
ls -la "$OUT_DIR"/*.prof
