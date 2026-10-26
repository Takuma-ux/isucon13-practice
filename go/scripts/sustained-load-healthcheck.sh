#!/usr/bin/env bash
# Holistic sustained load: loadbench + mid-run POST /api/initialize + heap/goroutine snapshots.
# Usage: ./go/scripts/sustained-load-healthcheck.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO_DIR="$ROOT/go"
OUT_DIR="${TMPDIR:-/tmp}/isucon-sustained-load-$$"
BIN="${TMPDIR:-/tmp}/isupipe-sustained-$$"
LOAD_DURATION="${LOAD_DURATION:-6m}"
LOAD_WORKERS="${LOAD_WORKERS:-40}"
PPROF="http://127.0.0.1:6060"
API="http://127.0.0.1:8080"
HOST_HDR="u.isucon.local"

mkdir -p "$OUT_DIR"

if ! mysqladmin ping -h127.0.0.1 -uisucon -pisucon &>/dev/null; then
	echo "MySQL not reachable" >&2
	exit 1
fi

# Fake pdnsutil for /api/initialize during load.
FAKE_PDNS="$(mktemp -d)"
printf '#!/bin/sh\nexit 0\n' >"$FAKE_PDNS/pdnsutil"
chmod +x "$FAKE_PDNS/pdnsutil"
export PATH="$FAKE_PDNS:$PATH"
export ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=127.0.0.1

snapshot() {
	local label="$1"
	local ts
	ts="$(date -u +%H%M%S)"
	echo "=== snapshot $label @ $(date -Iseconds) ===" | tee -a "$OUT_DIR/timeline.log"

	curl -sf "${PPROF}/debug/pprof/heap" -o "$OUT_DIR/heap-${label}-${ts}.prof" || echo "heap capture failed" >&2
	curl -sf "${PPROF}/debug/pprof/goroutine" -o "$OUT_DIR/goroutine-${label}-${ts}.prof" || true

	local gtxt="$OUT_DIR/goroutine-${label}-${ts}.txt"
	curl -sf "${PPROF}/debug/pprof/goroutine?debug=1" -o "$gtxt" || true
	local gcount=0
	if [[ -f "$gtxt" ]]; then
		gcount="$(grep -c '^goroutine ' "$gtxt" 2>/dev/null || echo 0)"
	fi
	if [[ -f "$OUT_DIR/goroutine-${label}-${ts}.prof" ]]; then
		gcount_pprof="$(go tool pprof -top -nodecount=1 -sample_index=goroutine "$OUT_DIR/goroutine-${label}-${ts}.prof" 2>/dev/null | awk '/^Total:/ {print $6; exit}')"
		[[ -n "$gcount_pprof" ]] && gcount="$gcount_pprof"
	fi

	local rss_kb="?"
	local pid="$APP_PID"
	if [[ -r "/proc/$pid/status" ]]; then
		rss_kb="$(awk '/^VmRSS:/ {print $2}' "/proc/$pid/status")"
	fi

	{
		echo "label=$label utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		echo "goroutines_pprof_debug1=$gcount"
		echo "vm_rss_kb=$rss_kb"
	} | tee -a "$OUT_DIR/metrics.tsv"

	if [[ -f "$OUT_DIR/heap-${label}-${ts}.prof" ]]; then
		go tool pprof -top -inuse_space -nodecount=20 "$OUT_DIR/heap-${label}-${ts}.prof" \
			>"$OUT_DIR/heap-top-${label}-${ts}.txt" 2>&1 || true
		head -25 "$OUT_DIR/heap-top-${label}-${ts}.txt" | tee -a "$OUT_DIR/timeline.log"
	fi
}

post_initialize() {
	local tag="$1"
	echo "--- POST /api/initialize ($tag) ---" | tee -a "$OUT_DIR/timeline.log"
	local t0
	t0=$(date +%s)
	local code
	code="$(curl -sS -o "$OUT_DIR/initialize-${tag}.json" -w "%{http_code}" \
		-X POST "$API/api/initialize" -H "Host: $HOST_HDR")"
	local elapsed=$(( $(date +%s) - t0 ))
	echo "initialize_${tag} http=${code} elapsed_s=${elapsed}" | tee -a "$OUT_DIR/metrics.tsv"
	if [[ "$code" != "200" ]]; then
		echo "initialize failed: $code body=$(head -c 200 "$OUT_DIR/initialize-${tag}.json")" >&2
	fi
}

echo "Building..."
(cd "$GO_DIR" && go build -o "$BIN" .)

pkill -f "isupipe-sustained-" 2>/dev/null || true
pkill -f "isupipe-pgo-verify" 2>/dev/null || true
if command -v fuser >/dev/null; then
	fuser -k 8080/tcp 6060/tcp 2>/dev/null || true
fi
sleep 1

echo "Starting app (PATH includes fake pdnsutil for init.sh)..."
( cd "$GO_DIR" && PATH="$FAKE_PDNS:$PATH" ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS=127.0.0.1 "$BIN" ) >"$OUT_DIR/app.log" 2>&1 &
APP_PID=$!
trap 'kill "$APP_PID" 2>/dev/null || true' EXIT

for i in $(seq 1 30); do
	if curl -sf "$API/api/tag" -H "Host: $HOST_HDR" >/dev/null 2>&1; then
		break
	fi
	sleep 1
done

echo "Bootstrap initialize..."
post_initialize bootstrap
if ! grep -q 'initialize_bootstrap http=200' "$OUT_DIR/metrics.tsv" 2>/dev/null; then
	echo "Bootstrap initialize did not return 200 — aborting (check $OUT_DIR/app.log)" >&2
	exit 1
fi

# loadbench logs in with password "test" — use test001 (verification user), not random top-comment streamer.
LS_ROW="$(mysql -h127.0.0.1 -uisucon -pisucon -N isupipe -e "
SELECT l.id, u.name FROM livestreams l
JOIN users u ON u.id = l.user_id
INNER JOIN livecomments lc ON lc.livestream_id = l.id
WHERE u.name = 'test001'
GROUP BY l.id, u.name ORDER BY COUNT(*) DESC LIMIT 1;")"
read -r LSID STREAMER <<<"$LS_ROW"
if [[ -z "$LSID" ]]; then
	LSID="999"
	STREAMER="test001"
fi

snapshot start

echo "Starting loadbench workers=$LOAD_WORKERS duration=$LOAD_DURATION livestream=$LSID..."
(
	cd "$GO_DIR"
	LOAD_WORKERS="$LOAD_WORKERS" LOAD_DURATION="$LOAD_DURATION" \
		LOAD_LIVESTREAM_ID="$LSID" LOAD_STREAMER="$STREAMER" \
		go run ./cmd/loadbench
) >"$OUT_DIR/loadbench.log" 2>&1 &
LOAD_PID=$!

# Mid-run initialize while load is active (bench-style session).
(
	sleep 90
	snapshot mid-90s
	post_initialize mid1
	sleep 120
	snapshot mid-210s
	post_initialize mid2
	sleep 90
	snapshot mid-300s
) &
MON_PID=$!

wait "$LOAD_PID" || true
kill "$MON_PID" 2>/dev/null || true
wait "$MON_PID" 2>/dev/null || true

snapshot end

MID_INIT_FAILED=0
for tag in mid1 mid2; do
	if ! grep -qE "initialize_${tag}[[:space:]]+http=200" "$OUT_DIR/metrics.tsv" 2>/dev/null; then
		MID_INIT_FAILED=1
	fi
done

echo ""
if [[ "$MID_INIT_FAILED" -eq 1 ]]; then
	echo "========================================================================"
	echo "WARNING: mid-run POST /api/initialize did NOT all return HTTP 200"
	echo "========================================================================"
	grep -E 'initialize_(mid1|mid2)[[:space:]]' "$OUT_DIR/metrics.tsv" 2>/dev/null || true
	echo "========================================================================"
	echo ""
fi
echo "Done. Artifacts: $OUT_DIR"
if [[ "$MID_INIT_FAILED" -eq 1 ]]; then
	echo "STATUS: COMPLETED WITH MID-INIT FAILURES (see WARNING above)"
else
	echo "STATUS: mid-run initialize calls OK (bootstrap + mid1 + mid2 where scheduled)"
fi
echo "--- metrics ---"
column -t -s $'\t' "$OUT_DIR/metrics.tsv" 2>/dev/null || cat "$OUT_DIR/metrics.tsv"
echo "--- loadbench tail ---"
tail -5 "$OUT_DIR/loadbench.log"
