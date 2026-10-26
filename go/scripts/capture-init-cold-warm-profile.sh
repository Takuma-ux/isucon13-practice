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

APP_PID=""
BIN_RESOLVED=""

pid_listening_on_port() {
	local port="$1"
	if command -v ss >/dev/null 2>&1; then
		ss -H -ltnp "sport = :${port}" 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | head -1
		return
	fi
	if command -v lsof >/dev/null 2>&1; then
		lsof -t -iTCP:"${port}" -sTCP:LISTEN 2>/dev/null | head -1
		return
	fi
	fuser -n tcp "${port}" 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i ~ /^[0-9]+$/) {print $i; exit}}'
}

verify_ports_owned_by_capture_binary() {
	local pid exe
	BIN_RESOLVED="$(readlink -f "$BIN")"
	for port in 8080 6060; do
		pid="$(pid_listening_on_port "$port")"
		if [[ -z "$pid" ]]; then
			echo "ERROR: nothing listening on :${port}; cannot capture profile from the expected isupipe process." >&2
			exit 1
		fi
		exe="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)"
		if [[ -z "$exe" || "$exe" != "$BIN_RESOLVED" ]]; then
			echo "ERROR: port ${port} is used by PID ${pid} (${exe:-unknown}), not this script's binary (${BIN_RESOLVED})." >&2
			echo "Stop the other process or free ports 8080/6060 before running this script." >&2
			exit 1
		fi
	done
}

start_capture_app_if_needed() {
	BIN_RESOLVED="$(readlink -f "$BIN")"
	local pid8080 pid6060 exe
	pid8080="$(pid_listening_on_port 8080)"
	if [[ -n "$pid8080" ]]; then
		exe="$(readlink -f "/proc/${pid8080}/exe" 2>/dev/null || true)"
		if [[ "$exe" != "$BIN_RESOLVED" ]]; then
			echo "ERROR: :8080 already in use by PID ${pid8080} (${exe:-unknown}), not ${BIN_RESOLVED}." >&2
			exit 1
		fi
		pid6060="$(pid_listening_on_port 6060)"
		if [[ -n "$pid6060" && "$pid6060" != "$pid8080" ]]; then
			exe="$(readlink -f "/proc/${pid6060}/exe" 2>/dev/null || true)"
			if [[ "$exe" != "$BIN_RESOLVED" ]]; then
				echo "ERROR: :6060 in use by PID ${pid6060} (${exe:-unknown}), not ${BIN_RESOLVED}." >&2
				exit 1
			fi
		fi
		APP_PID="$pid8080"
		echo "Using isupipe on :8080/:6060 (PID=$APP_PID, binary=$BIN_RESOLVED)"
		return
	fi
	echo "Starting app (cwd=$GO_DIR for ../img paths)..."
	( cd "$GO_DIR" && PATH="$FAKE_PDNS:$PATH" ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=127.0.0.1 "$BIN" ) >/tmp/isupipe-init-profile.log 2>&1 &
	APP_PID=$!
	trap 'kill "$APP_PID" 2>/dev/null || true' EXIT
	for _ in $(seq 1 30); do
		if curl -sf "http://127.0.0.1:8080/api/tag" -H "Host: u.isucon.local" >/dev/null 2>&1; then
			break
		fi
		sleep 1
	done
	verify_ports_owned_by_capture_binary
}

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

start_capture_app_if_needed

echo "POST /api/initialize (wipe tags + process caches)..."
INIT_CODE="$(curl -sS -X POST "http://127.0.0.1:8080/api/initialize" -H "Host: u.isucon.local" \
	-o "$OUT_DIR/initialize.json" -w "%{http_code}")"
echo "initialize_http=${INIT_CODE}"
if [[ "$INIT_CODE" != "200" ]]; then
	echo "ERROR: POST /api/initialize returned HTTP ${INIT_CODE}, expected 200." >&2
	echo "Body: $(head -c 500 "$OUT_DIR/initialize.json" 2>/dev/null || true)" >&2
	exit 1
fi

verify_ports_owned_by_capture_binary

echo "Starting loadbench immediately (workers=$LOAD_WORKERS duration=$LOAD_DURATION)..."
(
	cd "$GO_DIR"
	LOAD_WORKERS="$LOAD_WORKERS" LOAD_DURATION="$LOAD_DURATION" \
		LOAD_LIVESTREAM_ID="$LSID" LOAD_STREAMER="$STREAMER" \
		go run ./cmd/loadbench
) &
LOAD_PID=$!

echo "COLD window: CPU profile ${COLD_SEC}s (t=0 after initialize)..."
verify_ports_owned_by_capture_binary
curl -sS -o "$OUT_DIR/cpu-cold.prof" "http://127.0.0.1:6060/debug/pprof/profile?seconds=${COLD_SEC}"

echo "WARM_WAIT ${WARM_WAIT}s of continued load..."
sleep "$WARM_WAIT"

echo "WARM window: CPU profile ${WARM_SEC}s..."
verify_ports_owned_by_capture_binary
curl -sS -o "$OUT_DIR/cpu-warm.prof" "http://127.0.0.1:6060/debug/pprof/profile?seconds=${WARM_SEC}"

wait "$LOAD_PID" 2>/dev/null || true

echo "Profiles: $OUT_DIR/cpu-cold.prof $OUT_DIR/cpu-warm.prof"
ls -la "$OUT_DIR"/*.prof
