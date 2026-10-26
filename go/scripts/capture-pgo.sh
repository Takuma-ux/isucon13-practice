#!/usr/bin/env bash
# Capture a CPU profile via :6060 during cmd/loadbench and write go/default.pgo.
# Usage: from repo root, ./go/scripts/capture-pgo.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO_DIR="$ROOT/go"
BIN="${TMPDIR:-/tmp}/isupipe-pgo-capture"
PPROF_OUT="${TMPDIR:-/tmp}/isupipe-cpu.prof"
PROFILE_SEC="${PGO_PROFILE_SECONDS:-30}"
# Warmup before CPU capture so process caches (shell/user/tags) are hot — profile reflects post-caching paths, not cold-start stampedes.
PGO_WARMUP_SECONDS="${PGO_WARMUP_SECONDS:-60}"
LOAD_DURATION="${LOAD_DURATION:-120s}"
LOAD_WORKERS="${LOAD_WORKERS:-30}"

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

# Fail unless :8080 and :6060 are free or owned by the freshly built BIN.
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
			echo "Stop the other process or free ports 8080/6060 before running capture-pgo.sh." >&2
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
	echo "Starting app..."
	( cd "$GO_DIR" && ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=127.0.0.1 "$BIN" ) >/tmp/isupipe-pgo-capture.log 2>&1 &
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

if ! mysqladmin ping -h127.0.0.1 -uisucon -pisucon &>/dev/null; then
	echo "MySQL not reachable (isucon@127.0.0.1); start MySQL first." >&2
	exit 1
fi

LS_ROW="$(mysql -h127.0.0.1 -uisucon -pisucon -N isupipe -e "
SELECT l.id, u.name FROM livestreams l
JOIN users u ON u.id = l.user_id
INNER JOIN livecomments lc ON lc.livestream_id = l.id
GROUP BY l.id, u.name ORDER BY COUNT(*) DESC LIMIT 1;" 2>/dev/null)" || true
if [[ -z "$LS_ROW" ]]; then
	echo "No livestream with comments in DB; run /api/initialize or seed data first." >&2
	exit 1
fi
read -r LSID STREAMER <<<"$LS_ROW"

echo "Building app..."
(cd "$GO_DIR" && go build -o "$BIN" .)

start_capture_app_if_needed

echo "Load: workers=$LOAD_WORKERS duration=$LOAD_DURATION livestream=$LSID streamer=$STREAMER"
(
	cd "$GO_DIR"
	LOAD_WORKERS="$LOAD_WORKERS" LOAD_DURATION="$LOAD_DURATION" \
		LOAD_LIVESTREAM_ID="$LSID" LOAD_STREAMER="$STREAMER" \
		go run ./cmd/loadbench
) &
LOAD_PID=$!

echo "Warming caches (${PGO_WARMUP_SECONDS}s of load before profile capture)..."
sleep "$PGO_WARMUP_SECONDS"
verify_ports_owned_by_capture_binary
echo "Capturing CPU profile (${PROFILE_SEC}s) from :6060..."
curl -sS -o "$PPROF_OUT" "http://127.0.0.1:6060/debug/pprof/profile?seconds=${PROFILE_SEC}"

wait "$LOAD_PID" 2>/dev/null || true

go tool pprof -proto -output="$GO_DIR/default.pgo" "$PPROF_OUT"
META="$GO_DIR/default.pgo.meta"
GENERATED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
cat >"$META" <<EOF
# Metadata for default.pgo (NOT used by the Go compiler — for humans only)
#
# STATUS: INTERIM / SYNTHETIC — NOT production-validated
#
# Generated: ${GENERATED_UTC} via go/scripts/capture-pgo.sh
# Traffic:   cmd/loadbench (workers=${LOAD_WORKERS}, duration=${LOAD_DURATION})
# Warmup:    ${PGO_WARMUP_SECONDS}s load before ${PROFILE_SEC}s CPU capture (process shell/user caches hot)
# Profile:   reflects post-caching hot paths (not cold-cache startup burst)
# Source prof: $PPROF_OUT
#
# Replace from a REAL bench pprof capture before competition — see PGO.md
EOF
ls -la "$GO_DIR/default.pgo" "$META"
echo "Wrote $GO_DIR/default.pgo + $META — rebuild with: cd go && go build ."
echo "WARNING: loadbench profile is interim only; see PGO.md and default.pgo.meta"
